package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// DemoSeed is Dilnoza's reference ledger (dried fruits, Chorsu, YaTT), dated relative to
// now. On 18 Nov 2026 it yields exactly the plan's numbers: free 4 200 000, obligations
// 6 600 000 (rent 3 000 000 + supplier 3 100 000 + tax 500 000), reserve 1 200 000,
// profit 6 000 000, margin 12, owed to me 1 700 000 (Bekzod 21 days, Karim 14, Shoira 3).
// The current month's ops are clamped to today, so the numbers hold from the 15th of a
// month on (the hackathon runs 17–19 Nov).
//
// Counterparty ids in the returned state are stable keys ("bekzod"); ResetDemo maps them
// to real rows.
func DemoSeed(now time.Time) State {
	n := now.In(Tashkent)
	monthStart := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, Tashkent)
	in := func(monthsAgo, d, h int) time.Time {
		t := time.Date(n.Year(), n.Month()-time.Month(monthsAgo), d, h, 0, 0, 0, Tashkent)
		if monthsAgo == 0 && t.After(n) {
			t = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, Tashkent)
		}
		return t
	}
	st := State{
		Business: Business{Trade: "quruq meva", Sector: "dried_fruit", Market: "chorsu", Status: "yatt", Regime: "turnover", Reserve: 1_200_000},
		Counterparties: map[string]Counterparty{
			"bekzod": {Name: "Bekzod"}, "karim": {Name: "Karim"}, "shoira": {Name: "Shoira opa"}, "akmal": {Name: "Akmal aka"},
		},
	}
	seq := 0
	add := func(kind string, amount int64, method string, when time.Time, cp string) {
		seq++
		o := Op{ID: fmt.Sprintf("seed-%d", seq), Kind: kind, Amount: amount, Method: method, Source: "seed",
			OccurredAt: when, CreatedAt: when.Add(time.Duration(seq) * time.Millisecond)}
		if cp != "" {
			o.CounterpartyID = &cp
		}
		st.Ops = append(st.Ops, o)
	}
	// Three closed months, profit 6 000 000 each (Bekzod's nasiya 21 days ago is in the previous month).
	for ago := 3; ago >= 1; ago-- {
		sale := int64(30_000_000)
		if ago == 1 {
			sale -= 500_000
		}
		add(KindBuy, 18_000_000, MethodKarta, in(ago, 5, 10), "")
		add(KindSale, sale, MethodKarta, in(ago, 10, 10), "")
		add(KindExp, 6_000_000, MethodNaqd, in(ago, 20, 10), "")
	}
	add(KindNas, 500_000, MethodNone, n.AddDate(0, 0, -21), "bekzod")
	// This month: starting balances, then income 50 000 000 (48 800 000 sales + 1 200 000
	// nasiya), purchases 32 000 000, expenses 12 000 000 → naqd 4 800 000, karta 7 200 000.
	add(KindOpening, 6_800_000, MethodNaqd, monthStart, "")
	add(KindOpening, 400_000, MethodKarta, monthStart, "")
	add(KindSale, 10_000_000, MethodNaqd, in(0, 3, 10), "")
	add(KindSale, 38_800_000, MethodKarta, in(0, 6, 10), "")
	add(KindNas, 500_000, MethodNone, n.AddDate(0, 0, -14), "karim")
	add(KindNas, 700_000, MethodNone, n.AddDate(0, 0, -3), "shoira")
	add(KindBuy, 32_000_000, MethodKarta, in(0, 7, 10), "")
	add(KindExp, 12_000_000, MethodNaqd, in(0, 12, 10), "")

	rentDay, akmal, akmalName := 5, "akmal", "Akmal aka"
	supplierDue := time.Date(n.Year(), n.Month()+1, 10, 0, 0, 0, 0, Tashkent)
	st.Obligations = []Obligation{
		{ID: "rent", Kind: "rent", Label: "ijara", Amount: 3_000_000, DueDay: &rentDay, Recurring: true},
		{ID: "akmal", Kind: "supplier", Amount: 3_100_000, DueDate: &supplierDue, CounterpartyID: &akmal,
			Counterparty: &akmalName, CreatedAt: monthStart},
	}
	return st
}

var demoCounterpartyKinds = map[string]string{"bekzod": "customer", "karim": "customer", "shoira": "customer", "akmal": "supplier"}

// ResetDemo replaces all of the user's bookkeeping with DemoSeed(now), in one transaction.
func (r *PGRepo) ResetDemo(ctx context.Context, userID string, now time.Time) error {
	seed := DemoSeed(now)
	var facts []struct{ Key, Value string }
	raw, err := locales.ReadFile("locales/demo_memory.json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &facts); err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM ops WHERE user_id = $1`,
		`DELETE FROM obligations WHERE user_id = $1`,
		`DELETE FROM counterparties WHERE user_id = $1`,
		`DELETE FROM memory_facts WHERE user_id = $1`,
		`DELETE FROM business_status_history WHERE user_id = $1`,
		`DELETE FROM businesses WHERE user_id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return fmt.Errorf("reset: %w", err)
		}
	}
	b := seed.Business
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO businesses (user_id, trade, sector, market, status, regime, reserve) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		userID, b.Trade, b.Sector, b.Market, b.Status, b.Regime, b.Reserve); err != nil {
		return err
	}
	// The demo user has given both consents, so every screen (AI, Chamber) works.
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET consent_at = now(), consent_version = '2026-11', stats_consent_at = now(),
		        stats_consent_version = '2026-11', lang = 'uz' WHERE id = $1`, userID); err != nil {
		return err
	}
	ids := map[string]string{}
	for key, cp := range seed.Counterparties {
		var id string
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO counterparties (user_id, kind, name, name_key) VALUES ($1, $2, $3, $4) RETURNING id`,
			userID, demoCounterpartyKinds[key], cp.Name, NameKey(cp.Name)).Scan(&id); err != nil {
			return err
		}
		ids[key] = id
	}
	ref := func(p *string) sql.NullString {
		if p == nil {
			return sql.NullString{}
		}
		return sql.NullString{String: ids[*p], Valid: true}
	}
	for _, o := range seed.Ops {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ops (user_id, kind, amount, method, label, counterparty_id, source, occurred_at, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			userID, o.Kind, o.Amount, o.Method, o.Label, ref(o.CounterpartyID), o.Source, o.OccurredAt, o.CreatedAt); err != nil {
			return err
		}
	}
	for _, ob := range seed.Obligations {
		var dueDate sql.NullString
		if ob.DueDate != nil {
			dueDate = sql.NullString{String: ob.DueDate.Format(time.DateOnly), Valid: true}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO obligations (user_id, kind, label, amount, due_day, due_date, recurring, counterparty_id, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6::date, $7, $8, $9)`,
			userID, ob.Kind, ob.Label, ob.Amount, nullInt(ob.DueDay), dueDate, ob.Recurring, ref(ob.CounterpartyID), ob.CreatedAt); err != nil {
			return err
		}
	}
	for _, f := range facts {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_facts (user_id, key, value) VALUES ($1, $2, $3)`, userID, f.Key, f.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}
