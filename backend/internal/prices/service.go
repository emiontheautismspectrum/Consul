package prices

import (
	"context"
	"database/sql"
	"log/slog"
	"sort"
	"time"

	"finadvisor/internal/ledger"
)

const (
	// K is the minimum number of different users behind a market median (k-anonymity).
	K        = 5
	lookback = 30 * 24 * time.Hour
)

type Repo struct{ db *sql.DB }

func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

// Store saves observations; an existing (product, market, source, date) row is kept.
func (r *Repo) Store(ctx context.Context, source string, obs []Observation, n int) (int, error) {
	stored := 0
	for _, o := range obs {
		res, err := r.db.ExecContext(ctx,
			`INSERT INTO prices (product, name, unit, market, price, n, observed_at, source)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT (product, market, source, observed_at) DO UPDATE SET price = EXCLUDED.price, n = EXCLUDED.n`,
			o.Product, o.Name, o.Unit, o.Market, o.Price, n, o.ObservedAt.Format(time.DateOnly), source)
		if err != nil {
			return stored, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			stored++
		}
	}
	return stored, nil
}

// RefreshSource fetches from a source and stores what it returned.
func (r *Repo) RefreshSource(ctx context.Context, s Source) error {
	obs, err := s.Fetch(ctx)
	if err != nil {
		return err
	}
	n, err := r.Store(ctx, s.Name(), obs, 0)
	slog.InfoContext(ctx, "prices: source refreshed", "source", s.Name(), "observations", len(obs), "stored", n)
	return err
}

func median(v []int64) int64 {
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// RefreshUserMedians turns consenting users' purchases (receipt items, last 30 days) into
// anonymous medians per product and bazaar. A median exists only when at least K different
// users bought the product there: nobody's own price can be read from it.
func (r *Repo) RefreshUserMedians(ctx context.Context, now time.Time) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT o.user_id, b.market, it->>'name', COALESCE(NULLIF(it->>'unit', ''), 'kg'), (it->>'price')::bigint
		  FROM ops o
		  JOIN users u ON u.id = o.user_id AND u.stats_consent_at IS NOT NULL
		  JOIN businesses b ON b.user_id = o.user_id AND b.market <> 'other'
		  CROSS JOIN LATERAL jsonb_array_elements(o.items) it
		 WHERE o.kind = 'buy' AND o.deleted_at IS NULL AND jsonb_typeof(o.items) = 'array'
		   AND o.occurred_at >= $1 AND (it->>'price') ~ '^[0-9]{1,12}$' AND length(COALESCE(it->>'name', '')) > 0`,
		now.Add(-lookback))
	if err != nil {
		return err
	}
	defer rows.Close()
	type key struct{ product, market string }
	type agg struct {
		name, unit string
		users      map[string]bool
		prices     []int64
	}
	groups := map[key]*agg{}
	for rows.Next() {
		var user, market, name, unit string
		var price int64
		if err := rows.Scan(&user, &market, &name, &unit, &price); err != nil {
			return err
		}
		if price <= 0 {
			continue
		}
		k := key{ledger.NameKey(name), market}
		a := groups[k]
		if a == nil {
			a = &agg{name: name, unit: unit, users: map[string]bool{}}
			groups[k] = a
		}
		a.users[user] = true
		a.prices = append(a.prices, price)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var obs []Observation
	counts := map[key]int{}
	for k, a := range groups {
		if len(a.users) < K || k.product == "" {
			continue
		}
		obs = append(obs, Observation{Product: k.product, Name: a.name, Unit: a.unit, Market: k.market, Price: median(a.prices), ObservedAt: now})
		counts[k] = len(a.users)
	}
	for _, o := range obs {
		if _, err := r.db.ExecContext(ctx,
			`INSERT INTO prices (product, name, unit, market, price, n, observed_at, source)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'user_op')
			 ON CONFLICT (product, market, source, observed_at) DO UPDATE SET price = EXCLUDED.price, n = EXCLUDED.n`,
			o.Product, o.Name, o.Unit, o.Market, o.Price, counts[key{o.Product, o.Market}], o.ObservedAt.Format(time.DateOnly)); err != nil {
			return err
		}
	}
	slog.InfoContext(ctx, "prices: user medians refreshed", "products_markets", len(obs))
	return nil
}

// SeedSample inserts the demo bazaar prices for dried fruit (flagged source "seed").
func (r *Repo) SeedSample(ctx context.Context, now time.Time) error {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	type p struct {
		name   string
		chorsu int64
		oloy   int64
	}
	var obs []Observation
	for _, x := range []p{{"Oʻrik qoqi", 45_000, 43_500}, {"Mayiz", 38_000, 39_000}, {"Yongʻoq", 70_000, 68_000}, {"Bodom", 120_000, 116_000}} {
		for market, price := range map[string]int64{"chorsu": x.chorsu, "oloy": x.oloy} {
			obs = append(obs, Observation{Product: ledger.NameKey(x.name), Name: x.name, Unit: "kg", Market: market, Price: price, ObservedAt: day})
		}
	}
	_, err := r.Store(ctx, "seed", obs, 0)
	return err
}

// --- reading ---

type MarketPrice struct {
	Market     string `json:"market"`
	Price      int64  `json:"price"`
	N          int    `json:"n,omitempty"`
	Source     string `json:"source"`
	ObservedAt string `json:"observed_at"`
	Sample     bool   `json:"sample,omitempty"` // demo data, not a measurement
}

type Mine struct {
	Price int64  `json:"price"`
	At    string `json:"at"`
}

type Item struct {
	Product string        `json:"product"`
	Name    string        `json:"name"`
	Unit    string        `json:"unit"`
	Mine    *Mine         `json:"mine,omitempty"`
	Markets []MarketPrice `json:"markets"`
}

// Lookup returns prices for products matching query (empty = the ones this user bought plus
// the most common). The user's own latest unit price comes from their own receipts.
func (r *Repo) Lookup(ctx context.Context, userID, query string, limit int) ([]Item, error) {
	if limit < 1 || limit > 50 {
		limit = 30
	}
	q := ledger.NameKey(query)
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT ON (product, market, source) product, name, unit, market, price, n, observed_at, source
		  FROM prices
		 WHERE ($1 = '' OR product LIKE '%' || $1 || '%')
		 ORDER BY product, market, source, observed_at DESC`, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byProduct := map[string]*Item{}
	var order []string
	for rows.Next() {
		var product, name, unit, market, source string
		var price int64
		var n int
		var at time.Time
		if err := rows.Scan(&product, &name, &unit, &market, &price, &n, &at, &source); err != nil {
			return nil, err
		}
		it := byProduct[product]
		if it == nil {
			it = &Item{Product: product, Name: name, Unit: unit, Markets: []MarketPrice{}}
			byProduct[product] = it
			order = append(order, product)
		}
		it.Markets = append(it.Markets, MarketPrice{Market: market, Price: price, N: n, Source: source, ObservedAt: at.Format(time.DateOnly), Sample: source == "seed"})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The user's own latest unit price per product.
	mine, err := r.mine(ctx, userID)
	if err != nil {
		return nil, err
	}
	for p, m := range mine {
		if it := byProduct[p]; it != nil {
			mc := m
			it.Mine = &mc
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := byProduct[order[i]], byProduct[order[j]]
		if (a.Mine != nil) != (b.Mine != nil) {
			return a.Mine != nil
		}
		return a.Name < b.Name
	})
	if len(order) > limit {
		order = order[:limit]
	}
	out := make([]Item, 0, len(order))
	for _, p := range order {
		out = append(out, *byProduct[p])
	}
	return out, nil
}

func (r *Repo) mine(ctx context.Context, userID string) (map[string]Mine, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT it->>'name', (it->>'price')::bigint, o.occurred_at
		  FROM ops o CROSS JOIN LATERAL jsonb_array_elements(o.items) it
		 WHERE o.user_id = $1 AND o.kind = 'buy' AND o.deleted_at IS NULL AND jsonb_typeof(o.items) = 'array'
		   AND (it->>'price') ~ '^[0-9]{1,12}$'
		 ORDER BY o.occurred_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Mine{}
	for rows.Next() {
		var name string
		var price int64
		var at time.Time
		if err := rows.Scan(&name, &price, &at); err != nil {
			return nil, err
		}
		if k := ledger.NameKey(name); k != "" && price > 0 {
			out[k] = Mine{Price: price, At: at.In(ledger.Tashkent).Format(time.DateOnly)} // ORDER BY: the latest wins
		}
	}
	return out, rows.Err()
}

// Find adapts Lookup to consul.PriceLookup (the chat tool): at most five products.
type Finder struct{ Repo *Repo }

func (f Finder) Lookup(ctx context.Context, userID, query string) (any, error) {
	items, err := f.Repo.Lookup(ctx, userID, query, 5)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}
