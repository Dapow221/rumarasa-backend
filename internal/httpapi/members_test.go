package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"rumarasa-backend/internal/auth"
	"rumarasa-backend/internal/config"
	"rumarasa-backend/internal/db"
	"rumarasa-backend/internal/store"
)

// Integration tests against a real Postgres. They run only when
// TEST_DATABASE_URL points at a disposable database — never the dev one, as
// the members and reservations tables are truncated between tests.
func newTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE vouchers, members, reservations RESTART IDENTITY; ALTER SEQUENCE member_no_seq RESTART`); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Env: "dev", JWTSecret: []byte(strings.Repeat("s", 32))}
	token, err := auth.NewAccessToken(cfg.JWTSecret, 1)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(cfg, store.New(pool)).Handler(), token
}

type result struct {
	Status int
	Body   map[string]any
	Raw    string
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) result {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "203.0.113.7:4444"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := result{Status: rec.Code, Raw: rec.Body.String()}
	json.Unmarshal(rec.Body.Bytes(), &res.Body)
	return res
}

func validMember() map[string]any {
	return map[string]any{
		"name":     "Sekar Ayu",
		"phone":    "0812-3456-7890",
		"email":    "Sekar@Example.com",
		"birthday": "1995-04-12",
		"address":  "Jl. Kemang Raya No. 1, Jakarta Selatan",
		"consent":  true,
	}
}

func TestMemberSignupAndApproval(t *testing.T) {
	h, token := newTestServer(t)

	res := do(t, h, "POST", "/api/v1/members", "", validMember())
	if res.Status != http.StatusCreated {
		t.Fatalf("signup: got %d %s", res.Status, res.Raw)
	}
	if strings.Contains(res.Raw, "Sekar") {
		t.Errorf("public signup response must not echo PII: %s", res.Raw)
	}

	// Same number, different formatting, is still a duplicate.
	dup := validMember()
	dup["phone"] = "+62 812 3456 7890"
	if res := do(t, h, "POST", "/api/v1/members", "", dup); res.Status != http.StatusConflict {
		t.Fatalf("duplicate: got %d %s", res.Status, res.Raw)
	}

	if res := do(t, h, "GET", "/api/v1/admin/members", "", nil); res.Status != http.StatusUnauthorized {
		t.Fatalf("list without token: got %d", res.Status)
	}

	res = do(t, h, "GET", "/api/v1/admin/members?status=pending&q=sekar", token, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("list: got %d %s", res.Status, res.Raw)
	}
	list := res.Body["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("want 1 pending member, got %d", len(list))
	}
	m := list[0].(map[string]any)
	if m["phone"] != "6281234567890" || m["email"] != "sekar@example.com" || m["member_no"] != nil {
		t.Errorf("unexpected stored member: %v", m)
	}
	if meta := res.Body["meta"].(map[string]any); meta["total"] != float64(1) || meta["hasMore"] != false {
		t.Errorf("unexpected meta: %v", meta)
	}

	res = do(t, h, "PATCH", "/api/v1/admin/members/1", token, map[string]any{"status": "active", "tier": "gold"})
	if res.Status != http.StatusOK {
		t.Fatalf("approve: got %d %s", res.Status, res.Raw)
	}
	approved := res.Body["data"].(map[string]any)
	if approved["member_no"] != "RN000001" || approved["tier"] != "gold" {
		t.Errorf("approval should issue a member number: %v", approved)
	}

	// Suspending and reactivating keeps the original number.
	do(t, h, "PATCH", "/api/v1/admin/members/1", token, map[string]any{"status": "suspended"})
	res = do(t, h, "PATCH", "/api/v1/admin/members/1", token, map[string]any{"status": "active"})
	if no := res.Body["data"].(map[string]any)["member_no"]; no != "RN000001" {
		t.Errorf("member number changed on reactivation: %v", no)
	}

	if res := do(t, h, "PATCH", "/api/v1/admin/members/1", token, map[string]any{"tier": "diamond"}); res.Status != http.StatusBadRequest {
		t.Errorf("invalid tier: got %d", res.Status)
	}

	res = do(t, h, "GET", "/api/v1/admin/members/export", token, nil)
	if res.Status != http.StatusOK || !strings.Contains(res.Raw, "RN000001,Sekar Ayu,6281234567890") {
		t.Errorf("export: got %d %q", res.Status, res.Raw)
	}

	if res := do(t, h, "DELETE", "/api/v1/admin/members/1", token, nil); res.Status != http.StatusNoContent {
		t.Errorf("delete: got %d", res.Status)
	}
	if res := do(t, h, "DELETE", "/api/v1/admin/members/1", token, nil); res.Status != http.StatusNotFound {
		t.Errorf("delete again: got %d", res.Status)
	}
}

func TestMemberSignupValidation(t *testing.T) {
	h, token := newTestServer(t)

	bad := validMember()
	bad["phone"] = "12345"
	bad["email"] = "not-an-email"
	bad["birthday"] = time.Now().AddDate(0, 0, 1).Format(time.DateOnly)
	bad["consent"] = false
	res := do(t, h, "POST", "/api/v1/members", "", bad)
	if res.Status != http.StatusBadRequest {
		t.Fatalf("got %d %s", res.Status, res.Raw)
	}
	msg := res.Body["error"].(map[string]any)["message"].(string)
	for _, want := range []string{"phone", "email", "birthday", "consent"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %s: %s", want, msg)
		}
	}

	// A filled honeypot looks like success but stores nothing.
	bot := validMember()
	bot["website"] = "http://spam.example"
	if res := do(t, h, "POST", "/api/v1/members", "", bot); res.Status != http.StatusCreated {
		t.Fatalf("honeypot: got %d", res.Status)
	}
	res = do(t, h, "GET", "/api/v1/admin/members", token, nil)
	if n := len(res.Body["data"].([]any)); n != 0 {
		t.Errorf("honeypot submission was stored (%d rows)", n)
	}
}

func TestReservations(t *testing.T) {
	h, token := newTestServer(t)
	tomorrow := time.Now().AddDate(0, 0, 1).Format(time.DateOnly)

	res := do(t, h, "POST", "/api/v1/reservations", "", map[string]any{
		"name": "Budi", "phone": "081298765432", "date": tomorrow, "time": "19:30", "guests": 4,
		"request": "Kursi bayi",
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("create: got %d %s", res.Status, res.Raw)
	}

	res = do(t, h, "POST", "/api/v1/reservations", "", map[string]any{
		"name": "Budi", "phone": "081298765432", "date": "2020-01-01", "time": "25:00", "guests": 0,
	})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("invalid: got %d %s", res.Status, res.Raw)
	}

	res = do(t, h, "GET", "/api/v1/admin/summary", token, nil)
	if got := res.Body["data"].(map[string]any)["pending_reservations"]; got != float64(1) {
		t.Errorf("pending reservations: got %v", got)
	}

	res = do(t, h, "GET", "/api/v1/admin/reservations?from="+tomorrow+"&to="+tomorrow, token, nil)
	list := res.Body["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["time"] != "19:30" {
		t.Fatalf("list: %s", res.Raw)
	}

	res = do(t, h, "PATCH", "/api/v1/admin/reservations/1", token, map[string]any{"status": "confirmed"})
	if res.Status != http.StatusOK || res.Body["data"].(map[string]any)["status"] != "confirmed" {
		t.Fatalf("confirm: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "PATCH", "/api/v1/admin/reservations/1", token, map[string]any{"status": "lost"}); res.Status != http.StatusBadRequest {
		t.Errorf("invalid status: got %d", res.Status)
	}
	if res := do(t, h, "GET", "/api/v1/admin/reservations?limit=500", token, nil); res.Status != http.StatusBadRequest {
		t.Errorf("unbounded page size should be rejected: got %d", res.Status)
	}
}

func TestPublicFormRateLimit(t *testing.T) {
	h, _ := newTestServer(t)
	var last int
	for i := range 6 {
		m := validMember()
		m["phone"] = "08120000000" + string(rune('0'+i))
		last = do(t, h, "POST", "/api/v1/members", "", m).Status
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("6th submission from one IP: got %d, want 429", last)
	}
}

func TestClientIPBehindProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.9")
	if got := clientIP(req); got != "198.51.100.9" {
		t.Errorf("behind proxy: got %s", got)
	}
	req.RemoteAddr = "203.0.113.7:4444"
	if got := clientIP(req); got != "203.0.113.7" {
		t.Errorf("direct client must not be able to spoof XFF: got %s", got)
	}
}
