package ledger_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"finadvisor/internal/infrastructure/db"
	"finadvisor/internal/infrastructure/httpx"
	"finadvisor/internal/ledger"
	"finadvisor/migrations"
)

// These tests need a real PostgreSQL (migrations run against it). Set LEDGER_IT_DSN,
// e.g. against a throwaway container:
//
//	docker run -d --name ledger-it -p 55432:5432 -e POSTGRES_PASSWORD=it postgres:17-alpine
//	LEDGER_IT_DSN="postgres://postgres:it@127.0.0.1:55432/postgres?sslmode=disable" go test ./internal/ledger -run Integration -v
//	docker rm -f ledger-it
func itDB(t *testing.T) *dbHandle {
	dsn := os.Getenv("LEDGER_IT_DSN")
	if dsn == "" {
		t.Skip("LEDGER_IT_DSN not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	if err := db.Migrate(ctx, dsn, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return &dbHandle{t: t, database: database}
}

type dbHandle struct {
	t        *testing.T
	database *sql.DB
}

// --- helpers ---

func newUser(t *testing.T, h *dbHandle, email string) string {
	t.Helper()
	var id string
	if err := h.database.QueryRow(
		`INSERT INTO users (email, password_hash, salt) VALUES ($1, 'x', 'x') RETURNING id`, email).
		Scan(&id); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

type testServer struct {
	*httptest.Server
	t *testing.T
}

func newTestServer(t *testing.T, h *dbHandle) *testServer {
	t.Helper()
	handler := ledger.NewHandler(ledger.NewPGRepo(h.database), ledger.TaxPolicy{VAT6Pct: 6, VAT12Pct: 12})
	mux := http.NewServeMux()
	// Test-only auth: the user id comes from a header, everything else is real.
	as := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(httpx.WithUserID(r.Context(), r.Header.Get("X-Test-User"))))
		})
	}
	handler.Register(mux, as)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testServer{srv, t}
}

func (s *testServer) do(t *testing.T, user, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", user)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func decodeOpResult(t *testing.T, b []byte) (opID string, freeBefore, freeAfter int64) {
	t.Helper()
	var res struct {
		Op         struct{ ID string } `json:"op"`
		FreeBefore int64               `json:"free_before"`
		FreeAfter  int64               `json:"free_after"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("decode: %v (%s)", err, b)
	}
	return res.Op.ID, res.FreeBefore, res.FreeAfter
}

// --- tests ---

func TestIntegrationOpsFlow(t *testing.T) {
	h := itDB(t)
	srv := newTestServer(t, h)
	user := newUser(t, h, fmt.Sprintf("ops-%d@it.test", time.Now().UnixNano()))
	sale := map[string]any{"kind": "sale", "amount": 1_200_000, "method": "naqd"}

	status, body := srv.do(t, user, "POST", "/ops", sale, nil) // no Idempotency-Key
	if status != http.StatusBadRequest {
		t.Fatalf("POST without key: %d %s, want 400 idempotency_key_required", status, body)
	}

	status, body = srv.do(t, user, "POST", "/ops", sale, map[string]string{"Idempotency-Key": "k-1"})
	if status != http.StatusCreated {
		t.Fatalf("POST /ops: %d %s", status, body)
	}
	opID, _, freeAfter := decodeOpResult(t, body)
	if opID == "" || freeAfter != 1_200_000 {
		t.Fatalf("first response: op %q free_after %d", opID, freeAfter)
	}

	// Repeat with the same key: the stored response comes back, nothing is inserted twice.
	status, body = srv.do(t, user, "POST", "/ops", sale, map[string]string{"Idempotency-Key": "k-1"})
	if status != http.StatusOK {
		t.Fatalf("replay: %d %s, want 200", status, body)
	}
	replayID, _, _ := decodeOpResult(t, body)
	if replayID != opID {
		t.Errorf("replay returned op %q, want the original %q", replayID, opID)
	}

	status, body = srv.do(t, user, "GET", "/ledger/today", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("today: %d %s", status, body)
	}
	var today struct {
		View struct {
			Naqd int64 `json:"naqd"`
			Free int64 `json:"free"`
		} `json:"view"`
	}
	if err := json.Unmarshal(body, &today); err != nil {
		t.Fatal(err)
	}
	if today.View.Naqd != 1_200_000 {
		t.Errorf("naqd = %d, want 1 200 000 (the replay must not double the sale)", today.View.Naqd)
	}

	status, body = srv.do(t, user, "DELETE", "/ops/"+opID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("DELETE /ops: %d %s", status, body)
	}
	var undo struct {
		FreeBefore int64 `json:"free_before"`
		FreeAfter  int64 `json:"free_after"`
	}
	if err := json.Unmarshal(body, &undo); err != nil {
		t.Fatal(err)
	}
	if undo.FreeBefore != 1_200_000 || undo.FreeAfter != 0 {
		t.Errorf("undo: %+v, want free 1 200 000 → 0", undo)
	}
}

func TestIntegrationForeignDelete(t *testing.T) {
	h := itDB(t)
	srv := newTestServer(t, h)
	owner := newUser(t, h, fmt.Sprintf("owner-%d@it.test", time.Now().UnixNano()))
	stranger := newUser(t, h, fmt.Sprintf("stranger-%d@it.test", time.Now().UnixNano()))

	status, body := srv.do(t, owner, "POST", "/ops",
		map[string]any{"kind": "sale", "amount": 100, "method": "naqd"},
		map[string]string{"Idempotency-Key": "fk-1"})
	if status != http.StatusCreated {
		t.Fatalf("seed: %d %s", status, body)
	}
	opID, _, _ := decodeOpResult(t, body)

	if s, b := srv.do(t, stranger, "DELETE", "/ops/"+opID, nil, nil); s != http.StatusNotFound {
		t.Errorf("foreign DELETE: %d %s, want 404", s, b)
	}
	// The op must still be there and count toward the owner's cash.
	status, body = srv.do(t, owner, "GET", "/ledger/today", nil, nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"naqd":100`)) {
		t.Errorf("owner today after foreign delete: %d %s", status, body)
	}
}

func TestIntegrationCursorPagination(t *testing.T) {
	h := itDB(t)
	srv := newTestServer(t, h)
	user := newUser(t, h, fmt.Sprintf("page-%d@it.test", time.Now().UnixNano()))

	base := time.Date(2026, time.November, 1, 10, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		status, body := srv.do(t, user, "POST", "/ops", map[string]any{
			"kind": "sale", "amount": 100 * (i + 1), "method": "naqd",
			"occurred_at": base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
		}, map[string]string{"Idempotency-Key": fmt.Sprintf("pg-%d", i)})
		if status != http.StatusCreated {
			t.Fatalf("seed op %d: %d %s", i, status, body)
		}
	}

	var cursor string
	pages := 0
	for {
		url := "/ops?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		status, body := srv.do(t, user, "GET", url, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("GET /ops: %d %s", status, body)
		}
		var page struct {
			Items      []struct{ ID string } `json:"items"`
			NextCursor string                `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		pages++
		for _, it := range page.Items {
			if seen[it.ID] {
				t.Fatalf("op %q appeared twice across pages", it.ID)
			}
			seen[it.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 3 {
		t.Errorf("saw %d ops across pages, want 3", len(seen))
	}
}

func TestIntegrationConcurrentSameKey(t *testing.T) {
	h := itDB(t)
	srv := newTestServer(t, h)
	user := newUser(t, h, fmt.Sprintf("race-%d@it.test", time.Now().UnixNano()))
	sale := map[string]any{"kind": "sale", "amount": 100, "method": "naqd"}

	const n = 8
	type result struct {
		status int
		opID   string
	}
	results := make(chan result, n)
	for i := 0; i < n; i++ {
		go func() {
			status, body := srv.do(t, user, "POST", "/ops", sale, map[string]string{"Idempotency-Key": "race-1"})
			id, _, _ := decodeOpResult(t, body)
			results <- result{status, id}
		}()
	}
	created, ids := 0, map[string]bool{}
	for i := 0; i < n; i++ {
		r := <-results
		if r.status == http.StatusCreated {
			created++
		} else if r.status != http.StatusOK {
			t.Errorf("concurrent POST returned %d, want 201 or 200", r.status)
		}
		ids[r.opID] = true
	}
	if created != 1 || len(ids) != 1 {
		t.Errorf("concurrent same key: %d created, %d distinct ids, want exactly 1", created, len(ids))
	}
	var stored int
	// Scoped to this user: the test database is reused between runs.
	if err := h.database.QueryRow(`SELECT count(*) FROM ops WHERE user_id = $1 AND idempotency_key = 'race-1'`, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Errorf("rows with the key = %d, want 1", stored)
	}
}
