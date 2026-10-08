package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"rumarasa-backend/internal/mail"
	"rumarasa-backend/internal/store"
)

const emailTimeout = 45 * time.Second

// sendInBackground runs fn after the response has gone out, so a slow or
// failing email provider never holds up or breaks the request that caused it.
// Failures are logged; the record is already saved.
func (s *Server) sendInBackground(what string, memberID int, fn func(ctx context.Context) error) {
	if s.mailer == nil {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), emailTimeout)
		defer cancel()
		if err := fn(ctx); err != nil {
			slog.Error("email failed", "email", what, "member_id", memberID, "err", err)
			return
		}
		slog.Info("email sent", "email", what, "member_id", memberID)
	}()
}

// Wait blocks until background emails have finished, for graceful shutdown.
func (s *Server) Wait() { s.bg.Wait() }

func (s *Server) sendSignupReceived(m *store.Member) {
	s.sendInBackground("signup_received", m.ID, func(ctx context.Context) error {
		msg, err := mail.SignupReceived(m.Email, m.Name, m.Tier, s.cfg.SiteURL)
		if err != nil {
			return err
		}
		return s.mailer.Send(ctx, msg)
	})
}

// cardImage fetches the rendered card PNG from the website. Email still goes
// out without it (the card link is always included) if the site can't serve it.
func (s *Server) cardImage(ctx context.Context, token string) []byte {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.SiteURL+"/kartu/"+token+"/image", nil)
	if err != nil {
		return nil
	}
	res, err := s.http.Do(req)
	if err != nil {
		slog.Warn("card image fetch failed", "err", err)
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" {
		slog.Warn("card image fetch failed", "status", res.StatusCode, "content_type", res.Header.Get("Content-Type"))
		return nil
	}
	png, err := io.ReadAll(io.LimitReader(res.Body, 3<<20))
	if err != nil {
		return nil
	}
	return png
}

// sendMemberCard emails an active member their card, creating the card link
// first if they don't have one yet.
func (s *Server) sendMemberCard(ctx context.Context, m *store.Member) error {
	if m.CardToken == nil {
		var err error
		if m, err = s.store.CreateMemberCard(ctx, m.ID); err != nil {
			return err
		}
	}
	cardURL := s.cfg.SiteURL + "/kartu/" + *m.CardToken
	msg, err := mail.MemberCard(m.Email, m.Name, *m.MemberNo, m.Tier, cardURL, s.cfg.SiteURL, s.cardImage(ctx, *m.CardToken))
	if err != nil {
		return err
	}
	if err := s.mailer.Send(ctx, msg); err != nil {
		return err
	}
	_, err = s.store.MarkMemberCardSent(ctx, m.ID)
	return err
}

func (s *Server) handleEmailMemberCard(w http.ResponseWriter, r *http.Request) {
	if s.mailer == nil {
		respondError(w, http.StatusServiceUnavailable, "email_disabled", "Email is not configured on the server")
		return
	}
	id, ok := itemID(w, r)
	if !ok {
		return
	}
	m, err := s.store.GetMember(r.Context(), id)
	if err != nil {
		respondMemberCardErr(w, err)
		return
	}
	if m.Status != "active" {
		respondMemberCardErr(w, store.ErrNotActive)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), emailTimeout)
	defer cancel()
	if err := s.sendMemberCard(ctx, m); err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrNotActive) {
			respondMemberCardErr(w, err)
			return
		}
		slog.Error("email failed", "email", "member_card", "member_id", m.ID, "err", err)
		respondError(w, http.StatusBadGateway, "email_failed", fmt.Sprintf("Email could not be sent to %s", m.Email))
		return
	}
	m, err = s.store.GetMember(r.Context(), id)
	if err != nil {
		respondInternal(w, err)
		return
	}
	adminID, _ := adminIDFrom(r.Context())
	slog.Info("member card emailed", "admin_id", adminID, "member_id", m.ID)
	respondData(w, http.StatusOK, m)
}
