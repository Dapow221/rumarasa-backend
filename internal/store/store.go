package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// ---------- Content blocks ----------

type ContentBlock struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Store) ListContent(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM content_blocks ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Store) UpsertContent(ctx context.Context, key, value string) (*ContentBlock, error) {
	var b ContentBlock
	err := s.pool.QueryRow(ctx, `
		INSERT INTO content_blocks (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()
		RETURNING key, value, updated_at`, key, value,
	).Scan(&b.Key, &b.Value, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) DeleteContent(ctx context.Context, key string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM content_blocks WHERE key = $1`, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- Collections (metadata-driven CRUD) ----------

type FieldKind int

const (
	FieldText FieldKind = iota
	FieldBool
)

type Field struct {
	Name     string
	Kind     FieldKind
	Required bool // must be present and non-empty on create
}

type Collection struct {
	Path   string // URL segment, e.g. "member-benefits"
	Table  string // SQL table name, e.g. "member_benefits"
	Fields []Field
}

// Collections defines every list-shaped resource on the landing page. Adding a
// new section is one entry here plus a migration.
var Collections = []Collection{
	{Path: "dishes", Table: "dishes", Fields: []Field{
		{Name: "kind", Kind: FieldText, Required: true},
		{Name: "name", Kind: FieldText, Required: true},
		{Name: "price", Kind: FieldText},
		{Name: "description", Kind: FieldText},
		{Name: "image_url", Kind: FieldText},
	}},
	{Path: "promos", Table: "promos", Fields: []Field{
		{Name: "badge", Kind: FieldText},
		{Name: "title", Kind: FieldText, Required: true},
		{Name: "description", Kind: FieldText},
		{Name: "description_en", Kind: FieldText},
		{Name: "price", Kind: FieldText},
		{Name: "price_note", Kind: FieldText},
		{Name: "image_url", Kind: FieldText},
		{Name: "featured", Kind: FieldBool},
	}},
	{Path: "happenings", Table: "happenings", Fields: []Field{
		{Name: "schedule", Kind: FieldText},
		{Name: "title", Kind: FieldText, Required: true},
		{Name: "description", Kind: FieldText},
		{Name: "image_url", Kind: FieldText},
	}},
	{Path: "facilities", Table: "facilities", Fields: []Field{
		{Name: "title", Kind: FieldText, Required: true},
		{Name: "description", Kind: FieldText},
		{Name: "description_en", Kind: FieldText},
		{Name: "image_url", Kind: FieldText},
	}},
	{Path: "member-benefits", Table: "member_benefits", Fields: []Field{
		{Name: "highlight", Kind: FieldText, Required: true},
		{Name: "description", Kind: FieldText},
	}},
}

func CollectionByPath(path string) (Collection, bool) {
	for _, c := range Collections {
		if c.Path == path {
			return c, true
		}
	}
	return Collection{}, false
}

func (c Collection) field(name string) (Field, bool) {
	for _, f := range c.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

func (c Collection) selectColumns() string {
	cols := make([]string, 0, len(c.Fields)+2)
	cols = append(cols, "id")
	for _, f := range c.Fields {
		cols = append(cols, f.Name)
	}
	cols = append(cols, "sort", "updated_at")
	return strings.Join(cols, ", ")
}

// ValidateItem checks a JSON body against the collection's fields. On create,
// required fields must be present; on update any subset is allowed.
func (c Collection) ValidateItem(body map[string]any, isCreate bool) (map[string]any, error) {
	clean := make(map[string]any, len(body))
	for k, v := range body {
		if k == "sort" {
			n, ok := v.(float64)
			if !ok {
				return nil, fmt.Errorf("field %q must be a number", k)
			}
			clean[k] = int(n)
			continue
		}
		f, ok := c.field(k)
		if !ok {
			return nil, fmt.Errorf("unknown field %q", k)
		}
		switch f.Kind {
		case FieldText:
			str, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("field %q must be a string", k)
			}
			if len(str) > 10_000 {
				return nil, fmt.Errorf("field %q is too long (max 10000 chars)", k)
			}
			clean[k] = str
		case FieldBool:
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("field %q must be a boolean", k)
			}
			clean[k] = b
		}
	}
	if isCreate {
		for _, f := range c.Fields {
			if !f.Required {
				continue
			}
			str, _ := clean[f.Name].(string)
			if strings.TrimSpace(str) == "" {
				return nil, fmt.Errorf("field %q is required", f.Name)
			}
		}
	}
	if len(clean) == 0 {
		return nil, errors.New("no valid fields in request body")
	}
	return clean, nil
}

func (s *Store) ListItems(ctx context.Context, c Collection) ([]map[string]any, error) {
	q := fmt.Sprintf(`SELECT %s FROM %s ORDER BY sort, id`, c.selectColumns(), c.Table)
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return rowsToMaps(rows)
}

func (s *Store) CreateItem(ctx context.Context, c Collection, item map[string]any) (map[string]any, error) {
	cols := make([]string, 0, len(item))
	placeholders := make([]string, 0, len(item))
	args := make([]any, 0, len(item))
	i := 1
	for k, v := range item {
		cols = append(cols, k)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		args = append(args, v)
		i++
	}
	q := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) RETURNING %s`,
		c.Table, strings.Join(cols, ", "), strings.Join(placeholders, ", "), c.selectColumns())
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return oneRowToMap(rows)
}

func (s *Store) UpdateItem(ctx context.Context, c Collection, id int, item map[string]any) (map[string]any, error) {
	sets := make([]string, 0, len(item)+1)
	args := make([]any, 0, len(item)+1)
	i := 1
	for k, v := range item {
		sets = append(sets, fmt.Sprintf("%s = $%d", k, i))
		args = append(args, v)
		i++
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)
	q := fmt.Sprintf(`UPDATE %s SET %s WHERE id = $%d RETURNING %s`,
		c.Table, strings.Join(sets, ", "), i, c.selectColumns())
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return oneRowToMap(rows)
}

func (s *Store) DeleteItem(ctx context.Context, c Collection, id int) error {
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, c.Table), id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func rowsToMaps(rows pgx.Rows) ([]map[string]any, error) {
	fields := rows.FieldDescriptions()
	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(fields))
		for i, f := range fields {
			m[string(f.Name)] = values[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func oneRowToMap(rows pgx.Rows) (map[string]any, error) {
	items, err := rowsToMaps(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return items[0], nil
}

// ---------- Images ----------

type ImageMeta struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int       `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Store) CreateImage(ctx context.Context, id, filename, contentType string, data []byte) (*ImageMeta, error) {
	var m ImageMeta
	err := s.pool.QueryRow(ctx, `
		INSERT INTO images (id, filename, content_type, size_bytes, data)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, filename, content_type, size_bytes, created_at`,
		id, filename, contentType, len(data), data,
	).Scan(&m.ID, &m.Filename, &m.ContentType, &m.SizeBytes, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) GetImage(ctx context.Context, id string) (contentType string, data []byte, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT content_type, data FROM images WHERE id = $1`, id,
	).Scan(&contentType, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return contentType, data, err
}

func (s *Store) ListImages(ctx context.Context) ([]ImageMeta, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, filename, content_type, size_bytes, created_at
		FROM images ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ImageMeta, 0, 16)
	for rows.Next() {
		var m ImageMeta
		if err := rows.Scan(&m.ID, &m.Filename, &m.ContentType, &m.SizeBytes, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteImage(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM images WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- Admins & refresh tokens ----------

type Admin struct {
	ID           int
	Username     string
	PasswordHash string
}

func (s *Store) GetAdminByUsername(ctx context.Context, username string) (*Admin, error) {
	var a Admin
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, password_hash FROM admins WHERE username = $1`, username,
	).Scan(&a.ID, &a.Username, &a.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM admins`).Scan(&n)
	return n, err
}

func (s *Store) CreateAdmin(ctx context.Context, username, passwordHash string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO admins (username, password_hash) VALUES ($1, $2)`, username, passwordHash)
	return err
}

func (s *Store) InsertRefreshToken(ctx context.Context, tokenHash string, adminID int, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO refresh_tokens (token_hash, admin_id, expires_at)
		VALUES ($1, $2, $3)`, tokenHash, adminID, expiresAt)
	return err
}

// ConsumeRefreshToken atomically revokes a live token and returns its admin id.
// Expired, revoked, or unknown tokens return ErrNotFound.
func (s *Store) ConsumeRefreshToken(ctx context.Context, tokenHash string) (int, error) {
	var adminID int
	err := s.pool.QueryRow(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		RETURNING admin_id`, tokenHash,
	).Scan(&adminID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return adminID, err
}

// CleanupRefreshTokens removes tokens that are expired or were revoked more
// than a day ago, so the table never grows unbounded.
func (s *Store) CleanupRefreshTokens(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM refresh_tokens
		WHERE expires_at < now() OR revoked_at < now() - interval '1 day'`)
	return err
}
