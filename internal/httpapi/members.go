package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"rumarasa-backend/internal/store"
)

const (
	defaultPageSize = 25
	maxPageSize     = 100
)

// pagination reads ?page= (1-based) and ?limit= (capped at 100).
func pagination(w http.ResponseWriter, r *http.Request) (limit, offset, page int, ok bool) {
	q := r.URL.Query()
	page, limit = 1, defaultPageSize
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			respondError(w, http.StatusBadRequest, "validation", "page must be a positive integer")
			return 0, 0, 0, false
		}
		page = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPageSize {
			respondError(w, http.StatusBadRequest, "validation", "limit must be between 1 and 100")
			return 0, 0, 0, false
		}
		limit = n
	}
	return limit, (page - 1) * limit, page, true
}

// decodePublicForm reads a small JSON body into dst. Submissions that fill the
// hidden "website" honeypot are reported as bots so the caller can pretend to
// succeed without storing anything.
func decodePublicForm(w http.ResponseWriter, r *http.Request, dst any) (isBot, ok bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil {
		respondError(w, http.StatusBadRequest, "validation", "invalid request body")
		return false, false
	}
	var trap struct {
		Website string `json:"website"`
	}
	if json.Unmarshal(raw, &trap) == nil && trap.Website != "" {
		return true, true
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		respondError(w, http.StatusBadRequest, "validation", "invalid JSON body")
		return false, false
	}
	return false, true
}

// ---------- Public submissions ----------

func (s *Server) handleCreateMember(w http.ResponseWriter, r *http.Request) {
	var body store.NewMember
	isBot, ok := decodePublicForm(w, r, &body)
	if !ok {
		return
	}
	if isBot {
		respondData(w, http.StatusCreated, map[string]string{"status": "pending"})
		return
	}
	if err := body.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	m, err := s.store.CreateMember(r.Context(), body)
	if errors.Is(err, store.ErrConflict) {
		respondError(w, http.StatusConflict, "already_registered", "This phone number is already registered")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	slog.Info("member signup received", "member_id", m.ID)
	s.sendSignupReceived(m)
	// Only the status goes back: the public caller has no business reading the
	// stored record, and echoing it would turn this into a PII lookup.
	respondData(w, http.StatusCreated, map[string]string{"status": m.Status})
}

func (s *Server) handleCreateReservation(w http.ResponseWriter, r *http.Request) {
	var body store.NewReservation
	isBot, ok := decodePublicForm(w, r, &body)
	if !ok {
		return
	}
	if isBot {
		respondData(w, http.StatusCreated, map[string]string{"status": "pending"})
		return
	}
	if err := body.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	res, err := s.store.CreateReservation(r.Context(), body)
	if err != nil {
		respondInternal(w, err)
		return
	}
	slog.Info("reservation received", "reservation_id", res.ID)
	respondData(w, http.StatusCreated, map[string]string{"status": res.Status})
}

// ---------- Admin: members ----------

func memberFilter(w http.ResponseWriter, r *http.Request) (store.MemberFilter, bool) {
	q := r.URL.Query()
	f := store.MemberFilter{Status: q.Get("status"), Query: q.Get("q")}
	if f.Status != "" && !slices.Contains(store.MemberStatuses, f.Status) {
		respondError(w, http.StatusBadRequest, "validation", "status must be one of "+strings.Join(store.MemberStatuses, ", "))
		return f, false
	}
	if len(f.Query) > 200 {
		respondError(w, http.StatusBadRequest, "validation", "q is too long")
		return f, false
	}
	return f, true
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	f, ok := memberFilter(w, r)
	if !ok {
		return
	}
	var page int
	if f.Limit, f.Offset, page, ok = pagination(w, r); !ok {
		return
	}
	members, total, err := s.store.ListMembers(r.Context(), f)
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondList(w, members, listMeta{Total: total, Page: page, Limit: f.Limit, HasMore: f.Offset+len(members) < total})
}

func (s *Server) handleUpdateMember(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	var body store.MemberUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "validation", "invalid JSON body")
		return
	}
	if err := body.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	m, firstActivation, err := s.store.UpdateMember(r.Context(), id, body)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Member not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("member updated", "admin_id", adminID, "member_id", m.ID, "status", m.Status, "tier", m.Tier)
	if firstActivation {
		approved := *m
		s.sendInBackground("member_card", m.ID, func(ctx context.Context) error {
			return s.sendMemberCard(ctx, &approved)
		})
	}
	respondData(w, http.StatusOK, m)
}

func (s *Server) handleDeleteMember(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteMember(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Member not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("member deleted", "admin_id", adminID, "member_id", id)
	w.WriteHeader(http.StatusNoContent)
}

// csvCell neutralizes values that spreadsheet apps would run as formulas.
func csvCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func (s *Server) handleExportMembers(w http.ResponseWriter, r *http.Request) {
	f, ok := memberFilter(w, r)
	if !ok {
		return
	}
	filename := "members-" + time.Now().Format("20060102") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")

	cw := csv.NewWriter(w)
	cw.Write([]string{"member_no", "name", "phone", "email", "birthday", "address", "status", "tier", "registered_at"})
	err := s.store.EachMember(r.Context(), f, func(m *store.Member) error {
		no := ""
		if m.MemberNo != nil {
			no = *m.MemberNo
		}
		return cw.Write([]string{
			no, csvCell(m.Name), m.Phone, csvCell(m.Email), m.Birthday, csvCell(m.Address),
			m.Status, m.Tier, m.CreatedAt.Format(time.RFC3339),
		})
	})
	cw.Flush()
	if err == nil {
		err = cw.Error()
	}
	if err != nil {
		// Headers are already sent, so the client just gets a truncated file.
		slog.Error("export members", "err", err)
	}
}

// ---------- Admin: reservations ----------

func (s *Server) handleListReservations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ReservationFilter{Status: q.Get("status"), From: q.Get("from"), To: q.Get("to")}
	if f.Status != "" && !slices.Contains(store.ReservationStatuses, f.Status) {
		respondError(w, http.StatusBadRequest, "validation", "status must be one of "+strings.Join(store.ReservationStatuses, ", "))
		return
	}
	for name, v := range map[string]string{"from": f.From, "to": f.To} {
		if _, err := time.Parse(time.DateOnly, v); v != "" && err != nil {
			respondError(w, http.StatusBadRequest, "validation", name+" must be a date in YYYY-MM-DD format")
			return
		}
	}
	var page int
	var ok bool
	if f.Limit, f.Offset, page, ok = pagination(w, r); !ok {
		return
	}
	list, total, err := s.store.ListReservations(r.Context(), f)
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondList(w, list, listMeta{Total: total, Page: page, Limit: f.Limit, HasMore: f.Offset+len(list) < total})
}

func (s *Server) handleUpdateReservation(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "validation", "invalid JSON body")
		return
	}
	if !slices.Contains(store.ReservationStatuses, body.Status) {
		respondError(w, http.StatusBadRequest, "validation", "status must be one of "+strings.Join(store.ReservationStatuses, ", "))
		return
	}
	res, err := s.store.UpdateReservationStatus(r.Context(), id, body.Status)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Reservation not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("reservation updated", "admin_id", adminID, "reservation_id", res.ID, "status", res.Status)
	respondData(w, http.StatusOK, res)
}

func (s *Server) handleDeleteReservation(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteReservation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Reservation not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminSummary(w http.ResponseWriter, r *http.Request) {
	members, reservations, err := s.store.PendingCounts(r.Context())
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondData(w, http.StatusOK, map[string]int{
		"pending_members":      members,
		"pending_reservations": reservations,
	})
}

// ---------- Membership cards ----------

func respondMemberCardErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		respondError(w, http.StatusNotFound, "not_found", "Member not found")
	case errors.Is(err, store.ErrNotActive):
		respondError(w, http.StatusConflict, "not_active", "Only active members can get a card")
	default:
		respondInternal(w, err)
	}
}

// memberCardAction runs one admin card operation on the member in the path.
func memberCardAction(w http.ResponseWriter, r *http.Request, action string,
	fn func(ctx context.Context, id int) (*store.Member, error)) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	m, err := fn(r.Context(), id)
	if err != nil {
		respondMemberCardErr(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info(action, "admin_id", adminID, "member_id", m.ID)
	respondData(w, http.StatusOK, m)
}

func (s *Server) handleCreateMemberCard(w http.ResponseWriter, r *http.Request) {
	memberCardAction(w, r, "member card created", s.store.CreateMemberCard)
}

func (s *Server) handleDeleteMemberCard(w http.ResponseWriter, r *http.Request) {
	memberCardAction(w, r, "member card revoked", s.store.DeleteMemberCard)
}

func (s *Server) handleSendMemberCard(w http.ResponseWriter, r *http.Request) {
	memberCardAction(w, r, "member card sent", s.store.MarkMemberCardSent)
}

func (s *Server) handleGetMemberCard(w http.ResponseWriter, r *http.Request) {
	token, ok := linkToken(w, r, "Card not found")
	if !ok {
		return
	}
	c, err := s.store.GetMemberCard(r.Context(), token)
	if errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "not_found", "Card not found")
		return
	}
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondData(w, http.StatusOK, c)
}
