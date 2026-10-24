package itest

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sudekazan/ddo_proje/backend/internal/api"
	"github.com/sudekazan/ddo_proje/backend/internal/platform"
)

func server(t *testing.T, e *env) *httptest.Server {
	cfg := platform.Config{JWTSecret: []byte("integration-test-secret-0123456789abcdef"), AccessTTL: time.Minute,
		RefreshTTL: time.Hour, RequestTimeout: 10 * time.Second, Location: ist, CORSOrigin: "*"}
	srv := httptest.NewServer(api.NewRouter(cfg, e.pool, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, token string, body any, headers ...string) (int, map[string]any, http.Header) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func login(t *testing.T, srv *httptest.Server, email string) string {
	st, out, _ := call(t, srv, http.MethodPost, "/api/auth/login", "", map[string]string{"email": email, "password": passwordFix})
	if st != http.StatusOK {
		t.Fatalf("login %s: %d %v", email, st, out)
	}
	return out["access_token"].(string)
}

func TestHTTPIdempotentPaymentAndRBAC(t *testing.T) {
	e := setup(t)
	srv := server(t, e)
	if st, _, h := call(t, srv, http.MethodGet, "/health", "", nil); st != http.StatusOK || h.Get("X-DDO-Node") != "6736b1" {
		t.Fatalf("health %d node=%q", st, h.Get("X-DDO-Node"))
	}
	if st, _, _ := call(t, srv, http.MethodGet, "/_hz/6736", "", nil); st != http.StatusOK {
		t.Fatalf("/_hz/6736 = %d", st)
	}
	if st, out, _ := call(t, srv, http.MethodPost, "/api/auth/login", "", map[string]string{"email": "muhasebe@test.local", "password": "wrong"}); st != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d %v", st, out)
	}
	acc := login(t, srv, "muhasebe@test.local")
	pat := login(t, srv, "hasta@test.local")
	body := map[string]any{"patient_id": 1, "amount_kurus": 12345, "method": "card"}
	st1, p1, _ := call(t, srv, http.MethodPost, "/api/payments", acc, body, "Idempotency-Key", "k-1")
	st2, p2, h2 := call(t, srv, http.MethodPost, "/api/payments", acc, body, "Idempotency-Key", "k-1")
	if st1 != http.StatusCreated || st2 != http.StatusCreated || p1["id"] != p2["id"] || h2.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay mismatch: %d %v / %d %v", st1, p1, st2, p2)
	}
	var payments int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM "tn-payments"`).Scan(&payments); err != nil || payments != 1 {
		t.Fatalf("payments=%d", payments)
	}
	body["amount_kurus"] = 1
	if st, out, _ := call(t, srv, http.MethodPost, "/api/payments", acc, body, "Idempotency-Key", "k-1"); st != http.StatusUnprocessableEntity {
		t.Fatalf("reused key with different body: %d %v", st, out)
	}
	// RBAC: a patient cannot take payments or list patients, and sees only their own record.
	if st, out, _ := call(t, srv, http.MethodPost, "/api/payments", pat, body); st != http.StatusForbidden {
		t.Fatalf("patient payment: %d %v", st, out)
	}
	if st, _, _ := call(t, srv, http.MethodGet, "/api/patients", pat, nil); st != http.StatusForbidden {
		t.Fatalf("patient list: %d", st)
	}
	if st, _, _ := call(t, srv, http.MethodGet, "/api/patients/2/ledger", pat, nil); st != http.StatusForbidden {
		t.Fatalf("foreign ledger: %d", st)
	}
	if st, _, _ := call(t, srv, http.MethodGet, "/api/patients/1/ledger", pat, nil); st != http.StatusOK {
		t.Fatalf("own ledger: %d", st)
	}
	if st, out, _ := call(t, srv, http.MethodGet, "/api/patients", "", nil); st != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %v", st, out)
	} else if out["error"].(map[string]any)["code"] != "UNAUTHORIZED" {
		t.Fatalf("error format: %v", out)
	}
}
