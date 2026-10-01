package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"rumarasa-backend/internal/store"
)

// respondVoucherErr maps store errors from voucher operations to responses.
func respondVoucherErr(w http.ResponseWriter, err error) {
	var ve *store.VoucherError
	switch {
	case errors.Is(err, store.ErrNotFound):
		respondError(w, http.StatusNotFound, "not_found", "Voucher not found")
	case errors.As(err, &ve):
		respondError(w, http.StatusConflict, ve.Code, ve.Message)
	default:
		respondInternal(w, err)
	}
}

func (s *Server) handleListVouchers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.VoucherFilter{Status: q.Get("status"), Query: q.Get("q")}
	if f.Status != "" && !slices.Contains(store.VoucherFilters, f.Status) {
		respondError(w, http.StatusBadRequest, "validation", "status must be one of "+strings.Join(store.VoucherFilters, ", "))
		return
	}
	if len(f.Query) > 200 {
		respondError(w, http.StatusBadRequest, "validation", "q is too long")
		return
	}
	if v := q.Get("member_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			respondError(w, http.StatusBadRequest, "validation", "member_id must be a positive integer")
			return
		}
		f.MemberID = n
	}
	var page int
	var ok bool
	if f.Limit, f.Offset, page, ok = pagination(w, r); !ok {
		return
	}
	list, total, err := s.store.ListVouchers(r.Context(), f)
	if err != nil {
		respondInternal(w, err)
		return
	}
	respondList(w, list, listMeta{Total: total, Page: page, Limit: f.Limit, HasMore: f.Offset+len(list) < total})
}

func (s *Server) handleCreateVoucher(w http.ResponseWriter, r *http.Request) {
	var body store.NewVoucher
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "validation", "invalid JSON body")
		return
	}
	if err := body.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	v, err := s.store.CreateVoucher(r.Context(), body)
	switch {
	case errors.Is(err, store.ErrConflict):
		respondError(w, http.StatusConflict, "code_taken", "A voucher with this code already exists")
		return
	case errors.Is(err, store.ErrCodesExhausted):
		respondError(w, http.StatusConflict, "codes_exhausted", "All 100 voucher codes for this month are used")
		return
	case err != nil:
		respondVoucherErr(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("voucher created", "admin_id", adminID, "voucher_id", v.ID, "amount", v.Amount)
	w.Header().Set("Location", "/api/v1/admin/vouchers/"+strconv.Itoa(v.ID))
	respondData(w, http.StatusCreated, v)
}

func (s *Server) handleUpdateVoucher(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	var body store.VoucherUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "validation", "invalid JSON body")
		return
	}
	if err := body.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	v, err := s.store.UpdateVoucher(r.Context(), id, body)
	if err != nil {
		respondVoucherErr(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("voucher updated", "admin_id", adminID, "voucher_id", v.ID, "status", v.Status)
	respondData(w, http.StatusOK, v)
}

func (s *Server) handleSendVoucher(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	v, err := s.store.MarkVoucherSent(r.Context(), id)
	if err != nil {
		respondVoucherErr(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("voucher sent", "admin_id", adminID, "voucher_id", v.ID, "by_link", v.LinkToken != nil)
	respondData(w, http.StatusOK, v)
}

func (s *Server) handleRedeemVoucher(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	v, err := s.store.RedeemVoucher(r.Context(), id)
	if err != nil {
		respondVoucherErr(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("voucher redeemed", "admin_id", adminID, "voucher_id", v.ID, "member_id", v.Member.ID, "amount", v.Amount)
	respondData(w, http.StatusOK, v)
}

func (s *Server) handleDeleteVoucher(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteVoucher(r.Context(), id); err != nil {
		respondVoucherErr(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("voucher deleted", "admin_id", adminID, "voucher_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateVoucherLink(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	v, err := s.store.CreateVoucherLink(r.Context(), id)
	if err != nil {
		respondVoucherErr(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("voucher link created", "admin_id", adminID, "voucher_id", v.ID)
	respondData(w, http.StatusOK, v)
}

func (s *Server) handleDeleteVoucherLink(w http.ResponseWriter, r *http.Request) {
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	v, err := s.store.DeleteVoucherLink(r.Context(), id)
	if err != nil {
		respondVoucherErr(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("voucher link removed", "admin_id", adminID, "voucher_id", v.ID)
	respondData(w, http.StatusOK, v)
}

// ---------- Public: link vouchers ----------

var linkTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)

// linkToken reads the token from the path. Responses are never cached: the
// URL is a secret and the voucher's state changes once it's redeemed.
func linkToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	t := r.PathValue("token")
	if !linkTokenRe.MatchString(t) {
		respondError(w, http.StatusNotFound, "not_found", "Voucher not found")
		return "", false
	}
	return t, true
}

func (s *Server) handleGetVoucherByLink(w http.ResponseWriter, r *http.Request) {
	token, ok := linkToken(w, r)
	if !ok {
		return
	}
	v, err := s.store.GetVoucherByLink(r.Context(), token)
	if err != nil {
		respondVoucherErr(w, err)
		return
	}
	respondData(w, http.StatusOK, v)
}

func (s *Server) handleRedeemVoucherByLink(w http.ResponseWriter, r *http.Request) {
	token, ok := linkToken(w, r)
	if !ok {
		return
	}
	var body store.LinkRedemption
	isBot, ok := decodePublicForm(w, r, &body)
	if !ok {
		return
	}
	if isBot {
		respondError(w, http.StatusBadRequest, "validation", "invalid request")
		return
	}
	if err := body.Validate(); err != nil {
		respondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	v, err := s.store.RedeemVoucherByLink(r.Context(), token, body)
	if err != nil {
		respondVoucherErr(w, err)
		return
	}
	slog.Info("voucher redeemed by link")
	respondData(w, http.StatusOK, v)
}
