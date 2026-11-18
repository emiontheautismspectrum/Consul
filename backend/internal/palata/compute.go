// Package palata produces anonymous statistics for the Chamber of Commerce (problem 21):
// the share of sales without a receipt / QR among entrepreneurs who gave the statistics
// consent, by sector and market. Snapshots are precomputed; groups smaller than K are never
// stored or returned; amounts are rounded to 1 mln and shares to 1 point.
package palata

import (
	"math"
	"math/rand/v2"
	"sort"
)

const (
	K          = 5 // k-anonymity threshold
	MinSales   = 5 // sales in the window to be counted
	Bootstrap  = 1000
	seed1      = 42
	roundMoney = 1_000_000
)

// Entrepreneur is one consenting user's aggregate for a window. It never leaves this
// package: only group statistics are stored.
type Entrepreneur struct {
	Sector, Market string
	Official       bool  // status yatt / oob / mchj
	Turnover       int64 // sum of sales in the window
	Recorded       int64 // sum of sales with a receipt / QR / terminal (fiscal)
	Switched       bool  // changed status from none to official within the window
	Synthetic      bool  // demo (seed) data
}

func (e Entrepreneur) unrecorded() float64 {
	if e.Turnover <= 0 {
		return 0
	}
	return 1 - float64(e.Recorded)/float64(e.Turnover)
}

type Group struct {
	Key            string  `json:"key"`
	N              int     `json:"n"`
	UnrecordedPct  int     `json:"unrecorded_pct"`
	OfficialPct    int     `json:"official_pct"`
	MedianTurnover int64   `json:"median_turnover"`
	Switched       int     `json:"switched_to_official"`
	Synthetic      bool    `json:"-"`
	unrecorded     float64 // exact median, for the city estimate
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

func pct(v float64) int { return int(math.Round(v * 100)) }

// group summarizes entrepreneurs; ok is false below K (the group must not be published).
func group(key string, es []Entrepreneur) (Group, bool) {
	if len(es) < K {
		return Group{}, false
	}
	g := Group{Key: key, N: len(es)}
	u := make([]float64, len(es))
	t := make([]float64, len(es))
	official := 0
	for i, e := range es {
		u[i], t[i] = e.unrecorded(), float64(e.Turnover)
		if e.Official {
			official++
		}
		if e.Switched {
			g.Switched++
		}
		g.Synthetic = g.Synthetic || e.Synthetic
	}
	g.unrecorded = median(u)
	g.UnrecordedPct = pct(g.unrecorded)
	g.OfficialPct = int(math.Round(float64(official) * 100 / float64(len(es))))
	g.MedianTurnover = int64(math.Round(median(t)/roundMoney)) * roundMoney
	return g, true
}

func by(es []Entrepreneur, key func(Entrepreneur) string) map[string][]Entrepreneur {
	out := map[string][]Entrepreneur{}
	for _, e := range es {
		out[key(e)] = append(out[key(e)], e)
	}
	return out
}

func groups(es []Entrepreneur, key func(Entrepreneur) string) []Group {
	var out []Group
	for k, members := range by(es, key) {
		if g, ok := group(k, members); ok {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// CityEstimate weights sector medians by each sector's share of city trade (weights from
// config, an assumption to confirm with the Chamber) and gives a 95% bootstrap interval in
// percentage points. Only sectors with at least K people take part; weights are renormalized.
func CityEstimate(es []Entrepreneur, weights map[string]float64) (estimatePct, ciPP int) {
	sectors := by(es, func(e Entrepreneur) string { return e.Sector })
	type part struct {
		w float64
		u []float64
	}
	var parts []part
	var wsum float64
	for s, members := range sectors {
		w := weights[s]
		if len(members) < K || w <= 0 {
			continue
		}
		u := make([]float64, len(members))
		for i, e := range members {
			u[i] = e.unrecorded()
		}
		parts = append(parts, part{w, u})
		wsum += w
	}
	if wsum == 0 {
		return 0, 0
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].w > parts[j].w || (parts[i].w == parts[j].w && len(parts[i].u) < len(parts[j].u))
	})
	est := func(sample func([]float64) []float64) float64 {
		var v float64
		for _, p := range parts {
			v += p.w / wsum * median(sample(p.u))
		}
		return v
	}
	point := est(func(u []float64) []float64 { return u })

	rng := rand.New(rand.NewPCG(seed1, 0)) // fixed: the report is reproducible
	boot := make([]float64, Bootstrap)
	buf := []float64{}
	for b := range boot {
		boot[b] = est(func(u []float64) []float64 {
			buf = buf[:0]
			for range u {
				buf = append(buf, u[rng.IntN(len(u))])
			}
			return buf
		})
	}
	sort.Float64s(boot)
	lo, hi := boot[int(0.025*Bootstrap)], boot[int(0.975*Bootstrap)-1]
	return pct(point), int(math.Round((hi - lo) / 2 * 100))
}

// Snapshot is what Refresh stores for one window.
type Snapshot struct {
	All       Group
	CityPct   int
	CIpp      int
	Sectors   []Group
	Markets   []Group
	Synthetic bool
}

// Summarize builds the window snapshot. ok is false when fewer than K people qualify (then
// nothing at all is published for the window).
func Summarize(es []Entrepreneur, weights map[string]float64) (Snapshot, bool) {
	all, ok := group("all", es)
	if !ok {
		return Snapshot{}, false
	}
	s := Snapshot{All: all, Synthetic: all.Synthetic,
		Sectors: groups(es, func(e Entrepreneur) string { return e.Sector }),
		Markets: groups(es, func(e Entrepreneur) string { return e.Market })}
	s.CityPct, s.CIpp = CityEstimate(es, weights)
	return s, true
}

// Survey shares; nil when fewer than K people answered.
type SurveyShare struct {
	Answer string `json:"answer"`
	Pct    int    `json:"pct"`
}

func Survey(counts map[string]int) (int, []SurveyShare) {
	total := 0
	for _, c := range counts {
		total += c
	}
	if total < K {
		return 0, nil
	}
	out := []SurveyShare{}
	for a, c := range counts {
		out = append(out, SurveyShare{a, int(math.Round(float64(c) * 100 / float64(total)))})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Pct > out[j].Pct || (out[i].Pct == out[j].Pct && out[i].Answer < out[j].Answer)
	})
	return total, out
}
