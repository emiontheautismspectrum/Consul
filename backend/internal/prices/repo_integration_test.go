package prices_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"finadvisor/internal/infrastructure/db"
	"finadvisor/internal/prices"
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
	for _, q := range []string{`DELETE FROM prices`, `DELETE FROM users WHERE email LIKE 'pr-%'`} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func buyer(t *testing.T, d *sql.DB, tag string, consent bool, market, items string, now time.Time) string {
	t.Helper()
	var id string
	var c any
	if consent {
		c = now
	}
	if err := d.QueryRow(`INSERT INTO users (email, stats_consent_at) VALUES ($1, $2) RETURNING id`,
		fmt.Sprintf("pr-%s-%d@it.test", tag, time.Now().UnixNano()), c).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO businesses (user_id, sector, market, status) VALUES ($1, 'dried_fruit', $2, 'yatt')`, id, market); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO ops (user_id, kind, amount, method, items, occurred_at) VALUES ($1, 'buy', 1000000, 'naqd', $2::jsonb, $3)`, id, items, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIntegrationUserMediansNeedFiveBuyers(t *testing.T) {
	d := itDB(t)
	ctx := context.Background()
	now := time.Now()
	repo := prices.NewRepo(d)

	var first string
	for i, price := range []int{44_000, 45_000, 46_000, 47_000, 100_000} { // median 46 000, the outlier does not move it
		id := buyer(t, d, "qoqi", true, "chorsu", fmt.Sprintf(`[{"name":"Oʻrik qoqi","qty":10,"unit":"kg","price":%d}]`, price), now)
		if i == 0 {
			first = id
		}
	}
	for i := 0; i < 4; i++ { // only 4 buyers of mayiz: no median may exist
		buyer(t, d, "mayiz", true, "chorsu", `[{"name":"Mayiz","qty":5,"unit":"kg","price":37000}]`, now)
	}
	buyer(t, d, "nocons", false, "chorsu", `[{"name":"Mayiz","qty":5,"unit":"kg","price":1}]`, now) // no consent: ignored
	buyer(t, d, "junk", true, "oloy", `[{"name":"Bodom","price":"abc"},{"name":"","price":5}]`, now)

	if err := repo.RefreshUserMedians(ctx, now); err != nil {
		t.Fatal(err)
	}
	items, err := repo.Lookup(ctx, first, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]prices.Item{}
	for _, it := range items {
		by[it.Product] = it
	}
	q, ok := by["orikqoqi"]
	if !ok || len(q.Markets) != 1 || q.Markets[0].Price != 46_000 || q.Markets[0].N != 5 || q.Markets[0].Source != "user_op" {
		t.Fatalf("dried apricot = %+v", q)
	}
	if _, ok := by["mayiz"]; ok {
		t.Errorf("a median from 4 buyers must not exist: %+v", by["mayiz"])
	}
	if q.Mine == nil || q.Mine.Price != 44_000 {
		t.Errorf("the buyer must see their own price: %+v", q.Mine)
	}
	// Another user sees the market median but not anybody's own price.
	other, _ := repo.Lookup(ctx, "00000000-0000-0000-0000-000000000000", "qoqi", 5)
	if len(other) != 1 || other[0].Mine != nil {
		t.Errorf("foreign user: %+v", other)
	}
	// The database refuses a user median built on fewer than 5 users.
	if _, err := d.Exec(`INSERT INTO prices (product, name, unit, market, price, n, observed_at, source) VALUES ('x','X','kg','chorsu',1,4,now(),'user_op')`); err == nil {
		t.Error("the CHECK constraint must reject a user_op median with n < 5")
	}
	// Official and sample rows coexist with the medians.
	if err := repo.SeedSample(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store(ctx, "narx_stat", []prices.Observation{{Product: "orikqoqi", Name: "Oʻrik qoqi", Unit: "kg", Market: "republic", Price: 48_000, ObservedAt: now}}, 0); err != nil {
		t.Fatal(err)
	}
	items, _ = repo.Lookup(ctx, first, "qoqi", 5)
	if len(items) != 1 || len(items[0].Markets) < 3 {
		t.Errorf("sources must be shown side by side: %+v", items)
	}
}
