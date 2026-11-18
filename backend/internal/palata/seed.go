package palata

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

// SeedConfig tunes the synthetic population so the demo report shows the pitch numbers.
// These are demo data, flagged synthetic everywhere — never present them as a real survey.
type SeedConfig struct {
	Users       int
	OfficialPct float64
	Switched90  int
	Unrecorded  float64 // city-level centre of the unrecorded share
	Spread      float64 // per-person spread (uniform ± around the sector centre)
	TrendDrift  float64 // higher unrecorded share per month back in time
}

var DemoSeed = SeedConfig{Users: 812, OfficialPct: 0.58, Switched90: 18, Unrecorded: 0.31, Spread: 0.28, TrendDrift: 0.012}

var (
	seedSectors = []struct {
		key    string
		share  float64
		offset float64 // sector centre relative to the city; weighted offsets ≈ 0
	}{{"food", 0.35, 0.03}, {"clothes", 0.25, -0.02}, {"dried_fruit", 0.15, 0.01}, {"services", 0.15, -0.04}, {"other", 0.10, 0}}
	seedMarkets = []struct {
		key   string
		share float64
	}{{"chorsu", 0.30}, {"oloy", 0.25}, {"parkent", 0.15}, {"quyliq", 0.15}, {"other", 0.15}}
	surveyAnswers = []struct {
		key   string
		share float64
	}{{"fear_tax", 0.38}, {"complex_docs", 0.27}, {"no_benefit", 0.22}, {"other", 0.13}}
)

func pick[T any](rng *rand.Rand, items []T, share func(T) float64) T {
	x := rng.Float64()
	for _, it := range items {
		if x -= share(it); x < 0 {
			return it
		}
	}
	return items[len(items)-1]
}

// Seed replaces all synthetic entrepreneurs with a fresh, reproducible population.
func Seed(ctx context.Context, db *sql.DB, cfg SeedConfig, now time.Time) error {
	rng := rand.New(rand.NewPCG(seed1, 0))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE synthetic`); err != nil {
		return err
	}
	official := int(math.Round(cfg.OfficialPct * float64(cfg.Users)))
	perm := rng.Perm(cfg.Users) // perm[i] < official → official status
	const salesPerMonth, months = 20, 6

	var opRows []string
	var opArgs []any
	flush := func() error {
		if len(opRows) == 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO ops (user_id, kind, amount, method, fiscal, source, occurred_at) VALUES `+strings.Join(opRows, ","), opArgs...)
		opRows, opArgs = opRows[:0], opArgs[:0]
		return err
	}
	for i := 0; i < cfg.Users; i++ {
		sector := pick(rng, seedSectors, func(s struct {
			key           string
			share, offset float64
		}) float64 {
			return s.share
		})
		market := pick(rng, seedMarkets, func(m struct {
			key   string
			share float64
		}) float64 {
			return m.share
		})
		isOfficial := perm[i] < official
		switched := isOfficial && perm[i] < cfg.Switched90
		status := "none"
		if isOfficial {
			status = []string{"yatt", "yatt", "yatt", "oob", "mchj"}[rng.IntN(5)]
		}
		var id string
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO users (email, lang, synthetic, consent_at, consent_version, stats_consent_at, stats_consent_version, created_at)
			 VALUES ($1, 'uz', true, $2, '2026-11', $2, '2026-11', $2) RETURNING id`,
			fmt.Sprintf("seed-%04d@palata.seed", i), now.AddDate(0, 0, -200)).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO businesses (user_id, trade, sector, market, status, regime) VALUES ($1, 'seed', $2, $3, $4, 'turnover')`,
			id, sector.key, market.key, status); err != nil {
			return err
		}
		if switched {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO business_status_history (user_id, old_status, new_status, changed_at) VALUES ($1, 'none', $2, $3)`,
				id, status, now.AddDate(0, 0, -(1+rng.IntN(85)))); err != nil {
				return err
			}
		}
		if !isOfficial {
			a := pick(rng, surveyAnswers, func(s struct {
				key   string
				share float64
			}) float64 {
				return s.share
			})
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO survey_answers (user_id, question, answer) VALUES ($1, 'why_not_registered', $2)`, id, a.key); err != nil {
				return err
			}
		}
		u0 := cfg.Unrecorded + sector.offset + (rng.Float64()*2-1)*cfg.Spread
		monthly := 15e6 * math.Exp(rng.NormFloat64()*0.6+1) // median ≈ 41 mln a month
		perSale := int64(math.Round(monthly/salesPerMonth/1000)) * 1000
		for m := 0; m < months; m++ {
			u := math.Min(math.Max(u0+cfg.TrendDrift*float64(m), 0), 0.95)
			fiscal := int(math.Round((1 - u) * salesPerMonth))
			order := rng.Perm(salesPerMonth)
			for j := 0; j < salesPerMonth; j++ {
				at := now.Add(-time.Duration(float64(m*30)*24+float64(j)*36+1) * time.Hour)
				method := "naqd"
				if order[j] < fiscal {
					method = "karta"
				}
				// Varied amounts keep each person's recorded share continuous; equal amounts
				// would snap every share (and every median) to multiples of 5%.
				amount := int64(math.Round(float64(perSale)*(0.6+0.8*rng.Float64())/1000)) * 1000
				n := len(opArgs)
				opRows = append(opRows, fmt.Sprintf("($%d,'sale',$%d,$%d,$%d,'seed',$%d)", n+1, n+2, n+3, n+4, n+5))
				opArgs = append(opArgs, id, amount, method, order[j] < fiscal, at)
				if len(opRows) == 1000 {
					if err := flush(); err != nil {
						return err
					}
				}
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return tx.Commit()
}
