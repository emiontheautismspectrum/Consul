package palata_test

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
	"strings"
	"testing"
	"time"

	"finadvisor/internal/infrastructure/db"
	"finadvisor/internal/palata"
	"finadvisor/migrations"
)

// Needs PostgreSQL: LEDGER_IT_DSN (see internal/ledger/repo_integration_test.go).
func itDB(t *testing.T) *sql.DB {
	dsn := os.Getenv("LEDGER_IT_DSN")
	if dsn == "" {
		t.Skip("LEDGER_IT_DSN not set")
	}
	ctx := context.Background()
	if err := db.Migrate(ctx, dsn, migrations.FS); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	// isolated world for this test
	for _, q := range []string{`DELETE FROM palata_snapshots`, `DELETE FROM users WHERE email LIKE 'pit-%'`} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// entrepreneur creates a user with n sales (recorded share fiscalPct) in the last days.
func entrepreneur(t *testing.T, d *sql.DB, tag, sector, market string, consent bool, sales, fiscal int, now time.Time) string {
	t.Helper()
	var id string
	var c any
	if consent {
		c = now.AddDate(0, 0, -100)
	}
	if err := d.QueryRow(`INSERT INTO users (email, stats_consent_at) VALUES ($1, $2) RETURNING id`,
		fmt.Sprintf("pit-%s-%d@it.test", tag, time.Now().UnixNano()), c).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO businesses (user_id, sector, market, status) VALUES ($1, $2, $3, 'yatt')`, id, sector, market); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < sales; i++ {
		if _, err := d.Exec(`INSERT INTO ops (user_id, kind, amount, method, fiscal, occurred_at) VALUES ($1, 'sale', 1000000, 'naqd', $2, $3)`,
			id, i < fiscal, now.AddDate(0, 0, -(1+i))); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestIntegrationPalataRefreshAndReport(t *testing.T) {
	d := itDB(t)
	ctx := context.Background()
	now := time.Now()
	var food []string
	for i := 0; i < 6; i++ {
		food = append(food, entrepreneur(t, d, "food", "food", "chorsu", true, 10, 7, now))
	}
	for i := 0; i < 4; i++ { // a 4-person sector: never published
		entrepreneur(t, d, "cl", "clothes", "oloy", true, 10, 5, now)
	}
	entrepreneur(t, d, "noconsent", "food", "chorsu", false, 10, 0, now) // excluded: no consent
	entrepreneur(t, d, "fewsales", "food", "chorsu", true, 4, 0, now)    // excluded: < 5 sales

	repo := palata.NewRepo(d)
	if err := repo.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	palata.NewHandler(repo).Register(mux)
	get := func(path string) (int, []byte, http.Header) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		b, _ := io.ReadAll(rec.Body)
		return rec.Code, b, rec.Header()
	}
	code, body, _ := get("/palata/report?window=30")
	if code != 200 {
		t.Fatalf("report: %d %s", code, body)
	}
	var rep struct {
		KPI     map[string]any   `json:"kpi"`
		Sectors []map[string]any `json:"sectors"`
		Markets []map[string]any `json:"markets"`
	}
	json.Unmarshal(body, &rep)
	if rep.KPI["n"].(float64) != 10 {
		t.Errorf("n = %v, want 10 (no consent and < 5 sales excluded)", rep.KPI["n"])
	}
	for _, g := range append(rep.Sectors, rep.Markets...) {
		if g["key"] == "clothes" || g["key"] == "oloy" || g["n"].(float64) < 5 {
			t.Errorf("small group published: %v", g)
		}
	}
	if strings.Contains(string(body), "@") || strings.Contains(string(body), food[0]) {
		t.Error("report contains identifying data")
	}
	code, csv, h := get("/palata/report.csv?window=30")
	if code != 200 || !bytes.HasPrefix(csv, []byte{0xEF, 0xBB, 0xBF}) || !strings.Contains(string(csv), "section;key;n;") ||
		!strings.Contains(h.Get("Content-Type"), "text/csv") {
		t.Errorf("csv: %d %q", code, csv[:min(len(csv), 80)])
	}

	// Revoking consent removes the person from the next refresh.
	if _, err := d.Exec(`UPDATE users SET stats_consent_at = NULL WHERE id = $1`, food[0]); err != nil {
		t.Fatal(err)
	}
	if err := repo.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	_, body, _ = get("/palata/report?window=30")
	json.Unmarshal(body, &rep)
	if rep.KPI["n"].(float64) != 9 {
		t.Errorf("after revoking consent n = %v, want 9", rep.KPI["n"])
	}
	if code, _, _ := get("/palata/report?window=7"); code != 400 {
		t.Errorf("window=7: %d", code)
	}
}
