package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// VoucherError is a business-rule refusal (already redeemed, expired, member
// not active, …) that the API reports as a 409 with this code.
type VoucherError struct {
	Code    string
	Message string
}

func (e *VoucherError) Error() string { return e.Message }

func voucherErr(code, message string) error { return &VoucherError{Code: code, Message: message} }

// ErrCodesExhausted means all 100 codes for the current month are taken.
var ErrCodesExhausted = errors.New("all voucher codes for this month are used")

// Optional tells a JSON field that was left out apart from one sent as null.
type Optional[T any] struct {
	Set   bool
	Value *T
}

func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(b, []byte("null")) {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

var (
	VoucherStatuses = []string{"active", "redeemed", "void"}
	// VoucherFilters adds "expired": active vouchers past their last day.
	VoucherFilters = []string{"active", "expired", "redeemed", "void"}
	voucherCodeRe  = regexp.MustCompile(`^RR[0-9]{2}(0[1-9]|1[0-2])[0-9]{4}$`)
)

const jakartaToday = `(now() AT TIME ZONE 'Asia/Jakarta')::date`

type VoucherMember struct {
	ID       int     `json:"id"`
	MemberNo *string `json:"member_no"`
	Name     string  `json:"name"`
	Phone    string  `json:"phone"`
	Email    string  `json:"email"`
	Status   string  `json:"status"`
	Tier     string  `json:"tier"`
}

// VoucherRecipient is the non-member who redeemed a link voucher.
type VoucherRecipient struct {
	Name  string  `json:"name"`
	Phone *string `json:"phone"`
	Email string  `json:"email"`
}

type Voucher struct {
	ID          int               `json:"id"`
	Code        string            `json:"code"`
	Amount      int               `json:"amount"`
	Note        string            `json:"note"`
	ExpiresAt   *string           `json:"expires_at"`
	Status      string            `json:"status"`
	Expired     bool              `json:"expired"`
	Member      *VoucherMember    `json:"member"`
	LinkToken   *string           `json:"link_token"`
	Recipient   *VoucherRecipient `json:"recipient"`
	RedeemedVia *string           `json:"redeemed_via"`
	AssignedAt  *time.Time        `json:"assigned_at"`
	SentAt      *time.Time        `json:"sent_at"`
	RedeemedAt  *time.Time        `json:"redeemed_at"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

const voucherSelect = `SELECT v.id, v.code, v.amount, v.note, v.expires_at::text, v.status,
	COALESCE(v.status = 'active' AND v.expires_at < ` + jakartaToday + `, false),
	v.link_token, v.recipient_name, v.recipient_phone, v.recipient_email, v.redeemed_via,
	v.assigned_at, v.sent_at, v.redeemed_at, v.created_at, v.updated_at,
	m.id, m.member_no, m.name, m.phone, m.email, m.status, m.tier
	FROM vouchers v LEFT JOIN members m ON m.id = v.member_id`

func scanVoucher(row pgx.Row) (*Voucher, error) {
	var v Voucher
	var mID *int
	var mNo, mName, mPhone, mEmail, mStatus, mTier *string
	var rName, rPhone, rEmail *string
	err := row.Scan(&v.ID, &v.Code, &v.Amount, &v.Note, &v.ExpiresAt, &v.Status, &v.Expired,
		&v.LinkToken, &rName, &rPhone, &rEmail, &v.RedeemedVia,
		&v.AssignedAt, &v.SentAt, &v.RedeemedAt, &v.CreatedAt, &v.UpdatedAt,
		&mID, &mNo, &mName, &mPhone, &mEmail, &mStatus, &mTier)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if mID != nil {
		v.Member = &VoucherMember{ID: *mID, MemberNo: mNo, Name: *mName, Phone: *mPhone, Email: *mEmail, Status: *mStatus, Tier: *mTier}
	}
	if rName != nil {
		v.Recipient = &VoucherRecipient{Name: *rName, Phone: rPhone, Email: *rEmail}
	}
	return &v, nil
}

func (s *Store) GetVoucher(ctx context.Context, id int) (*Voucher, error) {
	return scanVoucher(s.pool.QueryRow(ctx, voucherSelect+` WHERE v.id = $1`, id))
}

func validateExpiry(v string) error {
	d, err := parseDate("expires_at", v)
	if err != nil {
		return err
	}
	if d.Before(todayInJakarta()) {
		return errors.New("expires_at cannot be in the past")
	}
	return nil
}

type NewVoucher struct {
	Code      string `json:"code"` // optional: generated when empty
	Amount    int    `json:"amount"`
	Note      string `json:"note"`
	ExpiresAt string `json:"expires_at"` // optional YYYY-MM-DD
	MemberID  *int   `json:"member_id"`  // optional: gift straight away
}

func (n *NewVoucher) Validate() error {
	var errs []string
	n.Code = strings.ToUpper(strings.TrimSpace(n.Code))
	if n.Code != "" && !voucherCodeRe.MatchString(n.Code) {
		errs = append(errs, "code must look like RR45012026 (RR + 2 digits + MM + YYYY)")
	}
	if n.Amount < 1000 || n.Amount > 100_000_000 {
		errs = append(errs, "amount must be between 1000 and 100000000 rupiah")
	}
	n.Note = strings.TrimSpace(n.Note)
	if len([]rune(n.Note)) > 500 {
		errs = append(errs, "note is too long (max 500 chars)")
	}
	n.ExpiresAt = strings.TrimSpace(n.ExpiresAt)
	if n.ExpiresAt != "" {
		if err := validateExpiry(n.ExpiresAt); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if n.MemberID != nil && *n.MemberID < 1 {
		errs = append(errs, "member_id is invalid")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// requireActiveMember locks the member row so they can't be suspended or
// deleted halfway through being given a voucher.
func requireActiveMember(ctx context.Context, tx pgx.Tx, id int) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM members WHERE id = $1 FOR SHARE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return voucherErr("member_not_found", "Member not found")
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return voucherErr("member_not_active", "Vouchers can only be given to active members")
	}
	return nil
}

// freeVoucherCode picks a random unused code for the current Jakarta month.
func freeVoucherCode(ctx context.Context, tx pgx.Tx) (string, error) {
	now := time.Now().In(jakarta)
	suffix := fmt.Sprintf("%02d%04d", int(now.Month()), now.Year())

	rows, err := tx.Query(ctx, `SELECT substr(code, 3, 2) FROM vouchers WHERE code LIKE 'RR__' || $1`, suffix)
	if err != nil {
		return "", err
	}
	used, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	free := make([]string, 0, 100)
	for i := range 100 {
		if d := fmt.Sprintf("%02d", i); !slices.Contains(used, d) {
			free = append(free, d)
		}
	}
	if len(free) == 0 {
		return "", ErrCodesExhausted
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(free))))
	if err != nil {
		return "", err
	}
	return "RR" + free[n.Int64()] + suffix, nil
}

// CreateVoucher issues a voucher, generating its code unless one was given.
// A given code that already exists returns ErrConflict.
func (s *Store) CreateVoucher(ctx context.Context, n NewVoucher) (*Voucher, error) {
	// Two admins can pick the same free code at once; the loser retries.
	for attempt := 0; ; attempt++ {
		id, err := s.insertVoucher(ctx, n)
		if isUniqueViolation(err) {
			if n.Code != "" {
				return nil, ErrConflict
			}
			if attempt < 3 {
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		return s.GetVoucher(ctx, id)
	}
}

func (s *Store) insertVoucher(ctx context.Context, n NewVoucher) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if n.MemberID != nil {
		if err := requireActiveMember(ctx, tx, *n.MemberID); err != nil {
			return 0, err
		}
	}
	code := n.Code
	if code == "" {
		if code, err = freeVoucherCode(ctx, tx); err != nil {
			return 0, err
		}
	}
	var expires *string
	if n.ExpiresAt != "" {
		expires = &n.ExpiresAt
	}
	var id int
	err = tx.QueryRow(ctx, `
		INSERT INTO vouchers (code, amount, note, expires_at, member_id, assigned_at)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5::int IS NULL THEN NULL ELSE now() END)
		RETURNING id`,
		code, n.Amount, n.Note, expires, n.MemberID).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

type VoucherFilter struct {
	Status   string // one of VoucherFilters
	Query    string
	MemberID int
	Limit    int
	Offset   int
}

// ListVouchers returns one page, newest first, with each voucher's member
// joined in.
func (s *Store) ListVouchers(ctx context.Context, f VoucherFilter) ([]Voucher, int, error) {
	conds := []string{"true"}
	args := []any{}
	switch f.Status {
	case "active":
		conds = append(conds, "v.status = 'active' AND (v.expires_at IS NULL OR v.expires_at >= "+jakartaToday+")")
	case "expired":
		conds = append(conds, "v.status = 'active' AND v.expires_at < "+jakartaToday)
	case "redeemed", "void":
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("v.status = $%d", len(args)))
	}
	if f.MemberID > 0 {
		args = append(args, f.MemberID)
		conds = append(conds, fmt.Sprintf("v.member_id = $%d", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+escapeLike(q)+"%")
		conds = append(conds, fmt.Sprintf(
			"(v.code ILIKE $%[1]d OR m.name ILIKE $%[1]d OR m.phone ILIKE $%[1]d OR m.member_no ILIKE $%[1]d"+
				" OR v.recipient_name ILIKE $%[1]d OR v.recipient_phone ILIKE $%[1]d OR v.recipient_email ILIKE $%[1]d)", len(args)))
	}
	where := strings.Join(conds, " AND ")

	var total int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM vouchers v LEFT JOIN members m ON m.id = v.member_id WHERE `+where, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`%s WHERE %s
		ORDER BY v.created_at DESC, v.id DESC
		LIMIT $%d OFFSET $%d`, voucherSelect, where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Voucher, 0, f.Limit)
	for rows.Next() {
		v, err := scanVoucher(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *v)
	}
	return out, total, rows.Err()
}

type VoucherUpdate struct {
	MemberID  Optional[int]    `json:"member_id"`  // null unassigns
	ExpiresAt Optional[string] `json:"expires_at"` // null removes the expiry
	Note      *string          `json:"note"`
	Status    *string          `json:"status"` // "active" or "void"
}

func (u *VoucherUpdate) Validate() error {
	if !u.MemberID.Set && !u.ExpiresAt.Set && u.Note == nil && u.Status == nil {
		return errors.New("provide member_id, expires_at, note and/or status")
	}
	var errs []string
	if u.MemberID.Value != nil && *u.MemberID.Value < 1 {
		errs = append(errs, "member_id is invalid")
	}
	if u.ExpiresAt.Value != nil {
		if err := validateExpiry(*u.ExpiresAt.Value); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if u.Note != nil {
		*u.Note = strings.TrimSpace(*u.Note)
		if len([]rune(*u.Note)) > 500 {
			errs = append(errs, "note is too long (max 500 chars)")
		}
	}
	if u.Status != nil && *u.Status != "active" && *u.Status != "void" {
		errs = append(errs, "status must be active or void (use redeem to mark a voucher used)")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// UpdateVoucher assigns/unassigns the member, changes the expiry or note, or
// voids/reactivates the voucher. A redeemed voucher is final.
func (s *Store) UpdateVoucher(ctx context.Context, id int, u VoucherUpdate) (*Voucher, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var status string
	var memberID *int
	var hasLink bool
	err = tx.QueryRow(ctx, `SELECT status, member_id, link_token IS NOT NULL FROM vouchers WHERE id = $1 FOR UPDATE`,
		id).Scan(&status, &memberID, &hasLink)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status == "redeemed" {
		return nil, voucherErr("already_redeemed", "This voucher has already been redeemed")
	}

	reassign := u.MemberID.Set && !equalPtr(u.MemberID.Value, memberID)
	if reassign && u.MemberID.Value != nil && hasLink {
		return nil, voucherErr("link_active", "This voucher is shared by link; remove the link before giving it to a member")
	}
	if reassign && u.MemberID.Value != nil {
		if err := requireActiveMember(ctx, tx, *u.MemberID.Value); err != nil {
			return nil, err
		}
	}

	// A new owner means the old WhatsApp message no longer applies.
	_, err = tx.Exec(ctx, `
		UPDATE vouchers SET
			member_id   = CASE WHEN $2 THEN $3 ELSE member_id END,
			assigned_at = CASE WHEN $2 THEN (CASE WHEN $3::int IS NULL THEN NULL ELSE now() END) ELSE assigned_at END,
			sent_at     = CASE WHEN $2 THEN NULL ELSE sent_at END,
			expires_at  = CASE WHEN $4 THEN $5::date ELSE expires_at END,
			note        = COALESCE($6, note),
			status      = COALESCE($7, status),
			updated_at  = now()
		WHERE id = $1`,
		id, reassign, u.MemberID.Value, u.ExpiresAt.Set, u.ExpiresAt.Value, u.Note, u.Status)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetVoucher(ctx, id)
}

func equalPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// MarkVoucherSent records that the owner sent the voucher to its member.
func (s *Store) MarkVoucherSent(ctx context.Context, id int) (*Voucher, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE vouchers SET sent_at = now(), updated_at = now()
		WHERE id = $1 AND (member_id IS NOT NULL OR link_token IS NOT NULL) AND status = 'active'`, id)
	if err != nil {
		return nil, err
	}
	v, err := s.GetVoucher(ctx, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if v.Member == nil && v.LinkToken == nil {
			return nil, voucherErr("not_assigned", "Give the voucher to a member or create a link before sending it")
		}
		return nil, voucherErr("not_active", "Only active vouchers can be sent")
	}
	return v, nil
}

// RedeemVoucher marks a voucher used at the cashier. The single conditional
// UPDATE makes double redemption impossible even with two tills at once.
func (s *Store) RedeemVoucher(ctx context.Context, id int) (*Voucher, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE vouchers v SET status = 'redeemed', redeemed_via = 'cashier', redeemed_at = now(), updated_at = now()
		FROM members m
		WHERE v.id = $1 AND v.status = 'active'
			AND (v.expires_at IS NULL OR v.expires_at >= `+jakartaToday+`)
			AND m.id = v.member_id AND m.status = 'active'`, id)
	if err != nil {
		return nil, err
	}
	v, err := s.GetVoucher(ctx, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 1 {
		return v, nil
	}
	switch {
	case v.Status == "redeemed":
		return nil, voucherErr("already_redeemed", "This voucher has already been redeemed")
	case v.Status == "void":
		return nil, voucherErr("voided", "This voucher has been voided")
	case v.Expired:
		return nil, voucherErr("expired", "This voucher has expired")
	case v.LinkToken != nil:
		return nil, voucherErr("link_voucher", "Link vouchers are redeemed by the customer on the website")
	case v.Member == nil:
		return nil, voucherErr("not_assigned", "This voucher is not assigned to a member")
	default:
		return nil, voucherErr("member_not_active", "The voucher's member is not active")
	}
}

// DeleteVoucher removes a voucher that was never sent or used — a typo fix.
// Anything a member may have seen must be voided instead, to keep the trail.
func (s *Store) DeleteVoucher(ctx context.Context, id int) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM vouchers WHERE id = $1 AND sent_at IS NULL AND status <> 'redeemed'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	if _, err := s.GetVoucher(ctx, id); err != nil {
		return err
	}
	return voucherErr("not_deletable", "Sent or redeemed vouchers can't be deleted; void them instead")
}

// ---------- Link vouchers (non-members) ----------

// CreateVoucherLink gives an unassigned voucher a fresh secret link token,
// replacing any earlier one so a leaked link can be revoked by re-creating it.
func (s *Store) CreateVoucherLink(ctx context.Context, id int) (*Voucher, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	tag, err := s.pool.Exec(ctx, `
		UPDATE vouchers SET link_token = $2, sent_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'active' AND member_id IS NULL`, id, token)
	if err != nil {
		return nil, err
	}
	v, err := s.GetVoucher(ctx, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if v.Member != nil {
			return nil, voucherErr("member_assigned", "This voucher is given to a member; remove the member first")
		}
		return nil, voucherErr("not_active", "Only active vouchers can be shared by link")
	}
	return v, nil
}

// DeleteVoucherLink turns the link off; anyone holding it gets "not found".
func (s *Store) DeleteVoucherLink(ctx context.Context, id int) (*Voucher, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE vouchers SET link_token = NULL, sent_at = NULL, updated_at = now()
		WHERE id = $1 AND status <> 'redeemed'`, id)
	if err != nil {
		return nil, err
	}
	v, err := s.GetVoucher(ctx, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, voucherErr("already_redeemed", "This voucher has already been redeemed")
	}
	return v, nil
}

// PublicVoucher is what the link holder sees. The code and recipient only
// appear once redeemed, as proof to show the cashier.
type PublicVoucher struct {
	Amount        int        `json:"amount"`
	ExpiresAt     *string    `json:"expires_at"`
	Note          string     `json:"note"`
	Status        string     `json:"status"` // active, expired, redeemed, void
	Code          *string    `json:"code"`
	RecipientName *string    `json:"recipient_name"`
	RedeemedAt    *time.Time `json:"redeemed_at"`
}

func toPublic(v *Voucher) *PublicVoucher {
	p := &PublicVoucher{Amount: v.Amount, ExpiresAt: v.ExpiresAt, Note: v.Note, Status: v.Status}
	if v.Expired {
		p.Status = "expired"
	}
	if v.Status == "redeemed" {
		p.Code = &v.Code
		p.RedeemedAt = v.RedeemedAt
		if v.Recipient != nil {
			p.RecipientName = &v.Recipient.Name
		}
	}
	return p
}

func (s *Store) GetVoucherByLink(ctx context.Context, token string) (*PublicVoucher, error) {
	v, err := scanVoucher(s.pool.QueryRow(ctx, voucherSelect+` WHERE v.link_token = $1`, token))
	if err != nil {
		return nil, err
	}
	return toPublic(v), nil
}

type LinkRedemption struct {
	Name    string `json:"name"`
	Phone   string `json:"phone"` // optional
	Email   string `json:"email"`
	Consent bool   `json:"consent"`
}

func (l *LinkRedemption) Validate() error {
	var errs []string
	var err error
	if l.Name, err = requireText("name", l.Name, 200); err != nil {
		errs = append(errs, err.Error())
	}
	l.Email = strings.ToLower(strings.TrimSpace(l.Email))
	if addr, err := mail.ParseAddress(l.Email); err != nil || addr.Address != l.Email || len(l.Email) > 254 {
		errs = append(errs, "email must be a valid email address")
	}
	if strings.TrimSpace(l.Phone) != "" {
		if l.Phone, err = NormalizePhone(l.Phone); err != nil {
			errs = append(errs, err.Error())
		}
	} else {
		l.Phone = ""
	}
	if !l.Consent {
		errs = append(errs, "consent to the privacy policy is required")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// RedeemVoucherByLink uses the voucher up on the customer's behalf. Like the
// cashier path, one conditional UPDATE guarantees it happens only once.
func (s *Store) RedeemVoucherByLink(ctx context.Context, token string, l LinkRedemption) (*PublicVoucher, error) {
	var phone *string
	if l.Phone != "" {
		phone = &l.Phone
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE vouchers SET status = 'redeemed', redeemed_via = 'link', redeemed_at = now(), updated_at = now(),
			recipient_name = $2, recipient_phone = $3, recipient_email = $4
		WHERE link_token = $1 AND status = 'active'
			AND (expires_at IS NULL OR expires_at >= `+jakartaToday+`)`,
		token, l.Name, phone, l.Email)
	if err != nil {
		return nil, err
	}
	v, err := s.GetVoucherByLink(ctx, token)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 1 {
		return v, nil
	}
	switch v.Status {
	case "redeemed":
		return nil, voucherErr("already_redeemed", "This voucher has already been redeemed")
	case "expired":
		return nil, voucherErr("expired", "This voucher has expired")
	default:
		return nil, voucherErr("voided", "This voucher has been voided")
	}
}
