package httpapi

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// signupActive registers a member and approves them, returning their id.
func signupActive(t *testing.T, h http.Handler, token, phone string) int {
	t.Helper()
	m := validMember()
	m["phone"] = phone
	if res := do(t, h, "POST", "/api/v1/members", "", m); res.Status != http.StatusCreated {
		t.Fatalf("signup: got %d %s", res.Status, res.Raw)
	}
	res := do(t, h, "GET", "/api/v1/admin/members?q="+phone[len(phone)-4:], token, nil)
	id := int(res.Body["data"].([]any)[0].(map[string]any)["id"].(float64))
	if res := do(t, h, "PATCH", fmt.Sprintf("/api/v1/admin/members/%d", id), token, map[string]any{"status": "active"}); res.Status != http.StatusOK {
		t.Fatalf("approve: got %d %s", res.Status, res.Raw)
	}
	return id
}

func errCode(res result) string {
	e, _ := res.Body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestVoucherGiftAndRedeem(t *testing.T) {
	h, token := newTestServer(t)
	memberA := signupActive(t, h, token, "081200000001")
	memberB := signupActive(t, h, token, "081200000002")

	if res := do(t, h, "POST", "/api/v1/admin/vouchers", "", map[string]any{"amount": 100000}); res.Status != http.StatusUnauthorized {
		t.Fatalf("create without token: got %d", res.Status)
	}

	// The owner's own code, unassigned.
	res := do(t, h, "POST", "/api/v1/admin/vouchers", token, map[string]any{"code": "rr45012026", "amount": 100000})
	if res.Status != http.StatusCreated {
		t.Fatalf("create: got %d %s", res.Status, res.Raw)
	}
	v := res.Body["data"].(map[string]any)
	if v["code"] != "RR45012026" || v["amount"] != float64(100000) || v["member"] != nil || v["status"] != "active" {
		t.Fatalf("unexpected voucher: %v", v)
	}
	id := int(v["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/vouchers/%d", id)

	if res := do(t, h, "POST", "/api/v1/admin/vouchers", token, map[string]any{"code": "RR45012026", "amount": 5000}); res.Status != http.StatusConflict {
		t.Fatalf("duplicate code: got %d %s", res.Status, res.Raw)
	}

	// Unassigned vouchers can't be sent or redeemed.
	if res := do(t, h, "POST", path+"/sent", token, nil); errCode(res) != "not_assigned" {
		t.Fatalf("send unassigned: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "POST", path+"/redeem", token, nil); errCode(res) != "not_assigned" {
		t.Fatalf("redeem unassigned: got %d %s", res.Status, res.Raw)
	}

	// Gift to A, send, then the owner changes their mind and gives it to B.
	res = do(t, h, "PATCH", path, token, map[string]any{"member_id": memberA})
	if res.Status != http.StatusOK || res.Body["data"].(map[string]any)["member"].(map[string]any)["phone"] != "6281200000001" {
		t.Fatalf("assign A: got %d %s", res.Status, res.Raw)
	}
	res = do(t, h, "POST", path+"/sent", token, nil)
	if res.Status != http.StatusOK || res.Body["data"].(map[string]any)["sent_at"] == nil {
		t.Fatalf("send: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "DELETE", path, token, nil); errCode(res) != "not_deletable" {
		t.Fatalf("delete sent voucher: got %d %s", res.Status, res.Raw)
	}
	res = do(t, h, "PATCH", path, token, map[string]any{"member_id": memberB})
	d := res.Body["data"].(map[string]any)
	if res.Status != http.StatusOK || d["member"].(map[string]any)["id"] != float64(memberB) || d["sent_at"] != nil {
		t.Fatalf("reassign to B should clear sent_at: got %d %s", res.Status, res.Raw)
	}

	// Suspended members can't receive or use vouchers.
	do(t, h, "PATCH", fmt.Sprintf("/api/v1/admin/members/%d", memberB), token, map[string]any{"status": "suspended"})
	if res := do(t, h, "POST", path+"/redeem", token, nil); errCode(res) != "member_not_active" {
		t.Fatalf("redeem for suspended member: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "PATCH", path, token, map[string]any{"member_id": memberB, "note": "x"}); res.Status != http.StatusOK {
		t.Fatalf("same member re-sent in patch must not re-check: got %d %s", res.Status, res.Raw)
	}
	res = do(t, h, "POST", "/api/v1/admin/vouchers", token, map[string]any{"amount": 50000, "member_id": memberB})
	if errCode(res) != "member_not_active" {
		t.Fatalf("create for suspended member: got %d %s", res.Status, res.Raw)
	}
	do(t, h, "PATCH", fmt.Sprintf("/api/v1/admin/members/%d", memberB), token, map[string]any{"status": "active"})

	res = do(t, h, "POST", path+"/redeem", token, nil)
	if res.Status != http.StatusOK || res.Body["data"].(map[string]any)["status"] != "redeemed" {
		t.Fatalf("redeem: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "POST", path+"/redeem", token, nil); errCode(res) != "already_redeemed" {
		t.Fatalf("double redeem: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "PATCH", path, token, map[string]any{"status": "active"}); errCode(res) != "already_redeemed" {
		t.Fatalf("edit redeemed: got %d %s", res.Status, res.Raw)
	}

	res = do(t, h, "GET", "/api/v1/admin/vouchers?status=redeemed&q=45012026", token, nil)
	if res.Status != http.StatusOK || len(res.Body["data"].([]any)) != 1 {
		t.Fatalf("list redeemed: got %d %s", res.Status, res.Raw)
	}
}

func TestVoucherGeneratedCodeAndValidation(t *testing.T) {
	h, token := newTestServer(t)
	member := signupActive(t, h, token, "081200000003")

	jkt, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Now().In(jkt)
	want := regexp.MustCompile(fmt.Sprintf(`^RR\d{2}%02d%04d$`, int(now.Month()), now.Year()))
	tomorrow := now.AddDate(0, 0, 1).Format(time.DateOnly)

	res := do(t, h, "POST", "/api/v1/admin/vouchers", token, map[string]any{
		"amount": 250000, "member_id": member, "expires_at": tomorrow, "note": "Ulang tahun",
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("create: got %d %s", res.Status, res.Raw)
	}
	v := res.Body["data"].(map[string]any)
	if !want.MatchString(v["code"].(string)) || v["expires_at"] != tomorrow || v["assigned_at"] == nil {
		t.Fatalf("unexpected generated voucher: %v", v)
	}

	for name, body := range map[string]map[string]any{
		"amount too small": {"amount": 500},
		"bad code":         {"amount": 10000, "code": "RR451320260"},
		"bad month":        {"amount": 10000, "code": "RR45132026"},
		"past expiry":      {"amount": 10000, "expires_at": "2020-01-01"},
	} {
		if res := do(t, h, "POST", "/api/v1/admin/vouchers", token, body); res.Status != http.StatusBadRequest {
			t.Errorf("%s: got %d %s", name, res.Status, res.Raw)
		}
	}

	// Never-sent vouchers can be deleted (typo fix); null clears the expiry.
	id := int(v["id"].(float64))
	res = do(t, h, "PATCH", fmt.Sprintf("/api/v1/admin/vouchers/%d", id), token, map[string]any{"expires_at": nil})
	if res.Status != http.StatusOK || res.Body["data"].(map[string]any)["expires_at"] != nil {
		t.Fatalf("clear expiry: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "DELETE", fmt.Sprintf("/api/v1/admin/vouchers/%d", id), token, nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete: got %d %s", res.Status, res.Raw)
	}
}

func TestVoucherCodesExhausted(t *testing.T) {
	h, token := newTestServer(t)
	for i := range 100 {
		if res := do(t, h, "POST", "/api/v1/admin/vouchers", token, map[string]any{"amount": 10000}); res.Status != http.StatusCreated {
			t.Fatalf("voucher %d: got %d %s", i, res.Status, res.Raw)
		}
	}
	if res := do(t, h, "POST", "/api/v1/admin/vouchers", token, map[string]any{"amount": 10000}); errCode(res) != "codes_exhausted" {
		t.Fatalf("101st voucher: got %d %s", res.Status, res.Raw)
	}
}

func TestVoucherLinkForNonMember(t *testing.T) {
	h, token := newTestServer(t)
	member := signupActive(t, h, token, "081200000009")

	res := do(t, h, "POST", "/api/v1/admin/vouchers", token, map[string]any{"amount": 100000, "note": "Untuk tamu"})
	id := int(res.Body["data"].(map[string]any)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/vouchers/%d", id)

	res = do(t, h, "POST", path+"/link", token, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("create link: got %d %s", res.Status, res.Raw)
	}
	link := res.Body["data"].(map[string]any)["link_token"].(string)
	if len(link) != 32 {
		t.Fatalf("unexpected token %q", link)
	}

	// Linked vouchers can't also go to a member, and only the customer redeems them.
	if res := do(t, h, "PATCH", path, token, map[string]any{"member_id": member}); errCode(res) != "link_active" {
		t.Fatalf("assign linked voucher: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "POST", path+"/redeem", token, nil); errCode(res) != "link_voucher" {
		t.Fatalf("cashier redeem of link voucher: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "POST", path+"/sent", token, nil); res.Status != http.StatusOK {
		t.Fatalf("mark link sent: got %d %s", res.Status, res.Raw)
	}

	pub := "/api/v1/vouchers/link/" + link
	res = do(t, h, "GET", pub, "", nil)
	d := res.Body["data"].(map[string]any)
	if res.Status != http.StatusOK || d["amount"] != float64(100000) || d["status"] != "active" || d["code"] != nil {
		t.Fatalf("public view before redeem must hide the code: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "GET", "/api/v1/vouchers/link/"+strings.Repeat("A", 32), "", nil); res.Status != http.StatusNotFound {
		t.Fatalf("unknown token: got %d", res.Status)
	}

	bad := map[string]any{"name": "Rina", "email": "nope", "consent": true}
	if res := do(t, h, "POST", pub+"/redeem", "", bad); res.Status != http.StatusBadRequest {
		t.Fatalf("bad email: got %d %s", res.Status, res.Raw)
	}
	good := map[string]any{"name": "Rina Wulandari", "email": "Rina@Example.com", "phone": "0813 1111 2222", "consent": true}
	res = do(t, h, "POST", pub+"/redeem", "", good)
	d = res.Body["data"].(map[string]any)
	if res.Status != http.StatusOK || d["status"] != "redeemed" || d["code"] == nil || d["recipient_name"] != "Rina Wulandari" {
		t.Fatalf("redeem by link: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "POST", pub+"/redeem", "", good); errCode(res) != "already_redeemed" {
		t.Fatalf("second redeem: got %d %s", res.Status, res.Raw)
	}

	res = do(t, h, "GET", "/api/v1/admin/vouchers?status=redeemed&q=wulandari", token, nil)
	list := res.Body["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("admin search by recipient: got %s", res.Raw)
	}
	v := list[0].(map[string]any)
	r := v["recipient"].(map[string]any)
	if v["redeemed_via"] != "link" || r["email"] != "rina@example.com" || r["phone"] != "6281311112222" {
		t.Fatalf("unexpected admin view: %v", v)
	}
	if res := do(t, h, "DELETE", path+"/link", token, nil); errCode(res) != "already_redeemed" {
		t.Fatalf("remove link after redeem: got %d %s", res.Status, res.Raw)
	}
}

func TestVoucherLinkRevoke(t *testing.T) {
	h, token := newTestServer(t)
	member := signupActive(t, h, token, "081200000008")

	res := do(t, h, "POST", "/api/v1/admin/vouchers", token, map[string]any{"amount": 50000, "member_id": member})
	id := int(res.Body["data"].(map[string]any)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/vouchers/%d", id)
	if res := do(t, h, "POST", path+"/link", token, nil); errCode(res) != "member_assigned" {
		t.Fatalf("link on member voucher: got %d %s", res.Status, res.Raw)
	}

	do(t, h, "PATCH", path, token, map[string]any{"member_id": nil})
	first := do(t, h, "POST", path+"/link", token, nil).Body["data"].(map[string]any)["link_token"].(string)
	second := do(t, h, "POST", path+"/link", token, nil).Body["data"].(map[string]any)["link_token"].(string)
	if first == second {
		t.Fatal("re-creating a link must rotate the token")
	}
	if res := do(t, h, "GET", "/api/v1/vouchers/link/"+first, "", nil); res.Status != http.StatusNotFound {
		t.Fatalf("old link after rotation: got %d", res.Status)
	}
	if res := do(t, h, "DELETE", path+"/link", token, nil); res.Status != http.StatusOK {
		t.Fatalf("remove link: got %d %s", res.Status, res.Raw)
	}
	if res := do(t, h, "GET", "/api/v1/vouchers/link/"+second, "", nil); res.Status != http.StatusNotFound {
		t.Fatalf("removed link: got %d", res.Status)
	}
}
