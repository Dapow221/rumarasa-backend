package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rumarasa-backend/internal/config"
	"rumarasa-backend/internal/mail"
)

type fakeMailer struct{ sent chan mail.Message }

func (f *fakeMailer) Send(_ context.Context, m mail.Message) error {
	f.sent <- m
	return nil
}

func (f *fakeMailer) next(t *testing.T) mail.Message {
	t.Helper()
	select {
	case m := <-f.sent:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no email was sent")
		return mail.Message{}
	}
}

// fakeSite stands in for the website's card image route.
func fakeSite(t *testing.T) *httptest.Server {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/kartu/") || !strings.HasSuffix(r.URL.Path, "/image") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG fake"))
	}))
	t.Cleanup(site.Close)
	return site
}

func TestMemberEmails(t *testing.T) {
	site := fakeSite(t)
	mailer := &fakeMailer{sent: make(chan mail.Message, 4)}
	h, token := newTestServerWith(t, &config.Config{SiteURL: site.URL}, mailer)

	signup := validMember()
	signup["name"] = "Sekar <b>Ayu</b>"
	signup["tier"] = "gold"
	if res := do(t, h, "POST", "/api/v1/members", "", signup); res.Status != http.StatusCreated {
		t.Fatalf("signup: got %d %s", res.Status, res.Raw)
	}
	m := mailer.next(t)
	if m.To != "sekar@example.com" || !strings.Contains(m.Subject, "Diterima") {
		t.Errorf("unexpected signup email: to=%s subject=%s", m.To, m.Subject)
	}
	if !strings.Contains(m.HTML, "Yth. Bapak/Ibu Sekar &lt;b&gt;Ayu&lt;/b&gt;,") || !strings.Contains(m.HTML, "<strong>Gold</strong>") {
		t.Errorf("signup email must greet the (escaped) name and show the tier:\n%s", m.HTML)
	}

	if res := do(t, h, "POST", "/api/v1/admin/members/1/card/email", token, nil); res.Status != http.StatusConflict {
		t.Fatalf("card email for pending member: got %d %s", res.Status, res.Raw)
	}

	// Approval sends the card on its own, creating the card link on the way.
	do(t, h, "PATCH", "/api/v1/admin/members/1", token, map[string]any{"status": "active"})
	m = mailer.next(t)
	if !strings.Contains(m.Subject, "Kartu Member") || !strings.Contains(m.Text, "RN000001") {
		t.Errorf("unexpected card email: %s\n%s", m.Subject, m.Text)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].ContentID != "kartu-member" || !strings.Contains(m.HTML, `src="cid:kartu-member"`) {
		t.Errorf("card image must be attached inline: %+v", m.Attachments)
	}
	if !strings.Contains(m.Text, site.URL+"/kartu/") {
		t.Errorf("card email must link to the card page:\n%s", m.Text)
	}

	// Later status changes don't re-send the welcome email.
	do(t, h, "PATCH", "/api/v1/admin/members/1", token, map[string]any{"status": "suspended"})
	do(t, h, "PATCH", "/api/v1/admin/members/1", token, map[string]any{"status": "active"})

	res := do(t, h, "POST", "/api/v1/admin/members/1/card/email", token, nil)
	if res.Status != http.StatusOK || res.Body["data"].(map[string]any)["card_sent_at"] == nil {
		t.Fatalf("manual card email: got %d %s", res.Status, res.Raw)
	}
	mailer.next(t)
	select {
	case extra := <-mailer.sent:
		t.Errorf("unexpected extra email: %s", extra.Subject)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestMemberEmailDisabled(t *testing.T) {
	h, token := newTestServer(t)
	do(t, h, "POST", "/api/v1/members", "", validMember())
	do(t, h, "PATCH", "/api/v1/admin/members/1", token, map[string]any{"status": "active"})
	if res := do(t, h, "POST", "/api/v1/admin/members/1/card/email", token, nil); res.Status != http.StatusServiceUnavailable {
		t.Fatalf("card email without mailer: got %d %s", res.Status, res.Raw)
	}
}
