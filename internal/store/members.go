package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrConflict = errors.New("conflict")

// jakarta is the restaurant's timezone; "today" for reservations and
// birthdays is always judged there, not in the server's zone.
var jakarta = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		return time.FixedZone("WIB", 7*60*60)
	}
	return loc
}()

// ---------- Validation helpers ----------

var nonDigits = regexp.MustCompile(`\D`)

// NormalizePhone turns "0812-3456-7890", "+62 812…" or "812…" into
// "628123456789" so the same person can't register twice with different
// formatting.
func NormalizePhone(raw string) (string, error) {
	d := nonDigits.ReplaceAllString(raw, "")
	switch {
	case strings.HasPrefix(d, "62"):
	case strings.HasPrefix(d, "0"):
		d = "62" + d[1:]
	case strings.HasPrefix(d, "8"):
		d = "62" + d
	}
	if !strings.HasPrefix(d, "62") || len(d) < 10 || len(d) > 15 {
		return "", errors.New("phone must be a valid Indonesian number")
	}
	return d, nil
}

func requireText(name, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len([]rune(v)) > max {
		return "", fmt.Errorf("%s is too long (max %d chars)", name, max)
	}
	return v, nil
}

func parseDate(name, v string) (time.Time, error) {
	d, err := time.ParseInLocation(time.DateOnly, strings.TrimSpace(v), jakarta)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be a date in YYYY-MM-DD format", name)
	}
	return d, nil
}

func todayInJakarta() time.Time {
	y, m, d := time.Now().In(jakarta).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, jakarta)
}

// escapeLike makes user search input match literally inside ILIKE.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ---------- Members ----------

var (
	MemberStatuses = []string{"pending", "active", "rejected", "suspended"}
	MemberTiers    = []string{"silver", "gold", "platinum"}
)

type Member struct {
	ID         int        `json:"id"`
	MemberNo   *string    `json:"member_no"`
	Name       string     `json:"name"`
	Phone      string     `json:"phone"`
	Email      string     `json:"email"`
	Birthday   string     `json:"birthday"`
	Address    string     `json:"address"`
	Status     string     `json:"status"`
	Tier       string     `json:"tier"`
	CardToken  *string    `json:"card_token"`
	CardSentAt *time.Time `json:"card_sent_at"`
	ConsentAt  time.Time  `json:"consent_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

const memberColumns = `id, member_no, name, phone, email, birthday::text, address,
	status, tier, card_token, card_sent_at, consent_at, created_at, updated_at`

func scanMember(row pgx.Row) (*Member, error) {
	var m Member
	err := row.Scan(&m.ID, &m.MemberNo, &m.Name, &m.Phone, &m.Email, &m.Birthday, &m.Address,
		&m.Status, &m.Tier, &m.CardToken, &m.CardSentAt, &m.ConsentAt, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

type NewMember struct {
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	Email    string `json:"email"`
	Birthday string `json:"birthday"`
	Address  string `json:"address"`
	Tier     string `json:"tier"` // requested tier; empty means silver
	Consent  bool   `json:"consent"`
}

// Validate trims and normalizes the signup in place, returning every problem
// at once so the form can show them together.
func (n *NewMember) Validate() error {
	var errs []string
	var err error
	if n.Name, err = requireText("name", n.Name, 200); err != nil {
		errs = append(errs, err.Error())
	}
	if n.Phone, err = NormalizePhone(n.Phone); err != nil {
		errs = append(errs, err.Error())
	}
	n.Email = strings.ToLower(strings.TrimSpace(n.Email))
	if addr, err := mail.ParseAddress(n.Email); err != nil || addr.Address != n.Email || len(n.Email) > 254 {
		errs = append(errs, "email must be a valid email address")
	}
	if d, err := parseDate("birthday", n.Birthday); err != nil {
		errs = append(errs, err.Error())
	} else if d.Year() < 1900 || !d.Before(todayInJakarta()) {
		errs = append(errs, "birthday must be a past date")
	}
	if n.Address, err = requireText("address", n.Address, 1000); err != nil {
		errs = append(errs, err.Error())
	}
	if n.Tier = strings.TrimSpace(n.Tier); n.Tier == "" {
		n.Tier = "silver"
	} else if !slices.Contains(MemberTiers, n.Tier) {
		errs = append(errs, fmt.Sprintf("tier must be one of %s", strings.Join(MemberTiers, ", ")))
	}
	if !n.Consent {
		errs = append(errs, "consent to the privacy policy is required")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// CreateMember stores a pending signup with the tier the applicant asked for;
// the admin confirms or changes it on approval. A phone number that is
// already registered returns ErrConflict.
func (s *Store) CreateMember(ctx context.Context, n NewMember) (*Member, error) {
	m, err := scanMember(s.pool.QueryRow(ctx, `
		INSERT INTO members (name, phone, email, birthday, address, tier, consent_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		RETURNING `+memberColumns,
		n.Name, n.Phone, n.Email, n.Birthday, n.Address, n.Tier))
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	return m, err
}

type MemberFilter struct {
	Status string
	Query  string
	Limit  int
	Offset int
}

func (f MemberFilter) where() (string, []any) {
	conds := []string{"true"}
	args := []any{}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+escapeLike(q)+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf(
			"(name ILIKE $%[1]d OR phone ILIKE $%[1]d OR email ILIKE $%[1]d OR member_no ILIKE $%[1]d)", n))
	}
	return strings.Join(conds, " AND "), args
}

// ListMembers returns one page of members, newest first, plus the total
// number of matches for the admin pager.
func (s *Store) ListMembers(ctx context.Context, f MemberFilter) ([]Member, int, error) {
	where, args := f.where()

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM members WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM members WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d OFFSET $%d`, memberColumns, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Member, 0, f.Limit)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *m)
	}
	return out, total, rows.Err()
}

// EachMember streams every matching member to fn without buffering the whole
// table, for CSV export.
func (s *Store) EachMember(ctx context.Context, f MemberFilter, fn func(*Member) error) error {
	where, args := f.where()
	rows, err := s.pool.Query(ctx, `SELECT `+memberColumns+` FROM members WHERE `+where+
		` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return err
		}
		if err := fn(m); err != nil {
			return err
		}
	}
	return rows.Err()
}

type MemberUpdate struct {
	Status *string `json:"status"`
	Tier   *string `json:"tier"`
}

func (u MemberUpdate) Validate() error {
	if u.Status == nil && u.Tier == nil {
		return errors.New("provide status and/or tier")
	}
	if u.Status != nil && !slices.Contains(MemberStatuses, *u.Status) {
		return fmt.Errorf("status must be one of %s", strings.Join(MemberStatuses, ", "))
	}
	if u.Tier != nil && !slices.Contains(MemberTiers, *u.Tier) {
		return fmt.Errorf("tier must be one of %s", strings.Join(MemberTiers, ", "))
	}
	return nil
}

// UpdateMember changes status and/or tier. The first time a member becomes
// active they are issued a permanent member number (RN000001, …).
func (s *Store) UpdateMember(ctx context.Context, id int, u MemberUpdate) (*Member, error) {
	return scanMember(s.pool.QueryRow(ctx, `
		UPDATE members SET
			status = COALESCE($2, status),
			tier = COALESCE($3, tier),
			member_no = CASE
				WHEN COALESCE($2, status) = 'active' AND member_no IS NULL
				THEN 'RN' || lpad(nextval('member_no_seq')::text, 6, '0')
				ELSE member_no END,
			updated_at = now()
		WHERE id = $1
		RETURNING `+memberColumns, id, u.Status, u.Tier))
}

func (s *Store) DeleteMember(ctx context.Context, id int) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM members WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- Membership cards ----------

// ErrNotActive means the member can't have a card link until they are active.
var ErrNotActive = errors.New("member not active")

// CreateMemberCard gives an active member a fresh secret card link, replacing
// any earlier one.
func (s *Store) CreateMemberCard(ctx context.Context, id int) (*Member, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	m, err := scanMember(s.pool.QueryRow(ctx, `
		UPDATE members SET card_token = $2, card_sent_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'active'
		RETURNING `+memberColumns, id, base64.RawURLEncoding.EncodeToString(raw)))
	if errors.Is(err, ErrNotFound) {
		if _, err := s.getMember(ctx, id); err != nil {
			return nil, err
		}
		return nil, ErrNotActive
	}
	return m, err
}

// DeleteMemberCard turns the card link off; anyone holding it gets "not found".
func (s *Store) DeleteMemberCard(ctx context.Context, id int) (*Member, error) {
	return scanMember(s.pool.QueryRow(ctx, `
		UPDATE members SET card_token = NULL, card_sent_at = NULL, updated_at = now()
		WHERE id = $1
		RETURNING `+memberColumns, id))
}

func (s *Store) MarkMemberCardSent(ctx context.Context, id int) (*Member, error) {
	return scanMember(s.pool.QueryRow(ctx, `
		UPDATE members SET card_sent_at = now(), updated_at = now()
		WHERE id = $1 AND card_token IS NOT NULL
		RETURNING `+memberColumns, id))
}

func (s *Store) getMember(ctx context.Context, id int) (*Member, error) {
	return scanMember(s.pool.QueryRow(ctx, `SELECT `+memberColumns+` FROM members WHERE id = $1`, id))
}

// MemberCard is what the card link shows: no contact details, so a forwarded
// link leaks nothing beyond the name on the card.
type MemberCard struct {
	Name     string    `json:"name"`
	MemberNo string    `json:"member_no"`
	Tier     string    `json:"tier"`
	Status   string    `json:"status"`
	JoinedAt time.Time `json:"joined_at"`
}

func (s *Store) GetMemberCard(ctx context.Context, token string) (*MemberCard, error) {
	var c MemberCard
	err := s.pool.QueryRow(ctx, `
		SELECT name, member_no, tier, status, created_at FROM members
		WHERE card_token = $1 AND member_no IS NOT NULL`, token,
	).Scan(&c.Name, &c.MemberNo, &c.Tier, &c.Status, &c.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ---------- Reservations ----------

var ReservationStatuses = []string{"pending", "confirmed", "cancelled", "completed", "no_show"}

type Reservation struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	Date      string    `json:"date"`
	Time      string    `json:"time"`
	Guests    int       `json:"guests"`
	Request   string    `json:"request"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const reservationColumns = `id, name, phone, date::text, to_char(time, 'HH24:MI'),
	guests, request, status, created_at, updated_at`

func scanReservation(row pgx.Row) (*Reservation, error) {
	var r Reservation
	err := row.Scan(&r.ID, &r.Name, &r.Phone, &r.Date, &r.Time,
		&r.Guests, &r.Request, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

type NewReservation struct {
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	Date    string `json:"date"`
	Time    string `json:"time"`
	Guests  int    `json:"guests"`
	Request string `json:"request"`
}

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func (n *NewReservation) Validate() error {
	var errs []string
	var err error
	if n.Name, err = requireText("name", n.Name, 200); err != nil {
		errs = append(errs, err.Error())
	}
	if n.Phone, err = NormalizePhone(n.Phone); err != nil {
		errs = append(errs, err.Error())
	}
	if d, err := parseDate("date", n.Date); err != nil {
		errs = append(errs, err.Error())
	} else if today := todayInJakarta(); d.Before(today) || d.After(today.AddDate(1, 0, 0)) {
		errs = append(errs, "date must be between today and one year ahead")
	}
	n.Time = strings.TrimSpace(n.Time)
	if !hhmm.MatchString(n.Time) {
		errs = append(errs, "time must be in HH:MM format")
	}
	if n.Guests < 1 || n.Guests > 500 {
		errs = append(errs, "guests must be between 1 and 500")
	}
	n.Request = strings.TrimSpace(n.Request)
	if len([]rune(n.Request)) > 1000 {
		errs = append(errs, "request is too long (max 1000 chars)")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (s *Store) CreateReservation(ctx context.Context, n NewReservation) (*Reservation, error) {
	return scanReservation(s.pool.QueryRow(ctx, `
		INSERT INTO reservations (name, phone, date, time, guests, request)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+reservationColumns,
		n.Name, n.Phone, n.Date, n.Time, n.Guests, n.Request))
}

type ReservationFilter struct {
	Status string
	From   string // inclusive, YYYY-MM-DD
	To     string // inclusive, YYYY-MM-DD
	Limit  int
	Offset int
}

// ListReservations returns one page ordered by visit date and time, so the
// admin sees the next arrivals first.
func (s *Store) ListReservations(ctx context.Context, f ReservationFilter) ([]Reservation, int, error) {
	conds := []string{"true"}
	args := []any{}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.From != "" {
		args = append(args, f.From)
		conds = append(conds, fmt.Sprintf("date >= $%d", len(args)))
	}
	if f.To != "" {
		args = append(args, f.To)
		conds = append(conds, fmt.Sprintf("date <= $%d", len(args)))
	}
	where := strings.Join(conds, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM reservations WHERE %s
		ORDER BY date, time, id
		LIMIT $%d OFFSET $%d`, reservationColumns, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Reservation, 0, f.Limit)
	for rows.Next() {
		r, err := scanReservation(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

func (s *Store) UpdateReservationStatus(ctx context.Context, id int, status string) (*Reservation, error) {
	return scanReservation(s.pool.QueryRow(ctx, `
		UPDATE reservations SET status = $2, updated_at = now()
		WHERE id = $1
		RETURNING `+reservationColumns, id, status))
}

func (s *Store) DeleteReservation(ctx context.Context, id int) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM reservations WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PendingCounts powers the admin badges: how many signups and reservations
// are still waiting for a decision.
func (s *Store) PendingCounts(ctx context.Context) (members, reservations int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM members WHERE status = 'pending'),
			(SELECT count(*) FROM reservations WHERE status = 'pending' AND date >= $1)`,
		todayInJakarta().Format(time.DateOnly),
	).Scan(&members, &reservations)
	return members, reservations, err
}
