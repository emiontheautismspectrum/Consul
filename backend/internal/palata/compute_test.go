package palata

import (
	"testing"
)

func people(n int, sector, market string, official bool, turnover, recorded int64) []Entrepreneur {
	out := make([]Entrepreneur, n)
	for i := range out {
		out[i] = Entrepreneur{Sector: sector, Market: market, Official: official, Turnover: turnover, Recorded: recorded}
	}
	return out
}

func TestKAnonymityInEveryCut(t *testing.T) {
	var es []Entrepreneur
	es = append(es, people(6, "food", "chorsu", true, 10_000_000, 7_000_000)...)
	es = append(es, people(4, "clothes", "oloy", false, 10_000_000, 5_000_000)...) // a group of 4
	snap, ok := Summarize(es, map[string]float64{"food": 0.5, "clothes": 0.5})
	if !ok || snap.All.N != 10 {
		t.Fatalf("all = %+v %v", snap.All, ok)
	}
	for _, g := range append(snap.Sectors, snap.Markets...) {
		if g.N < K {
			t.Errorf("group %q with n=%d published", g.Key, g.N)
		}
		if g.Key == "clothes" || g.Key == "oloy" {
			t.Errorf("4-person group %q must be absent", g.Key)
		}
	}
	if len(snap.Sectors) != 1 || len(snap.Markets) != 1 {
		t.Errorf("sectors %+v markets %+v", snap.Sectors, snap.Markets)
	}
	// The city estimate only uses sectors with >= K people.
	if snap.CityPct != 30 {
		t.Errorf("city = %d, want 30 (food only)", snap.CityPct)
	}
	if _, ok := Summarize(people(4, "food", "chorsu", true, 1, 1), nil); ok {
		t.Error("with 4 people nothing at all may be published")
	}
}

func TestGroupMetrics(t *testing.T) {
	es := append(people(3, "food", "chorsu", true, 40_000_000, 30_000_000), people(2, "food", "chorsu", false, 61_400_000, 0)...)
	es[0].Switched = true
	g, ok := group("food", es)
	if !ok {
		t.Fatal("5 people must be a group")
	}
	// unrecorded: 25%, 25%, 25%, 100%, 100% → median 25; official 3/5 = 60; turnover median 40 mln.
	if g.UnrecordedPct != 25 || g.OfficialPct != 60 || g.MedianTurnover != 40_000_000 || g.Switched != 1 {
		t.Errorf("group = %+v", g)
	}
	es[4].Turnover = 61_499_999
	if g, _ := group("x", es); g.MedianTurnover%1_000_000 != 0 {
		t.Errorf("median turnover must be rounded to 1 mln: %d", g.MedianTurnover)
	}
}

func TestCityEstimateBootstrap(t *testing.T) {
	var es []Entrepreneur
	for i := 0; i < 42; i++ { // two full cycles: symmetric around the median
		rec := int64(60 + i%21) // 60..80% recorded → 20..40% unrecorded, median 30
		es = append(es, Entrepreneur{Sector: "food", Turnover: 100, Recorded: rec})
		es = append(es, Entrepreneur{Sector: "clothes", Turnover: 100, Recorded: rec})
	}
	w := map[string]float64{"food": 0.7, "clothes": 0.3}
	p1, ci1 := CityEstimate(es, w)
	p2, ci2 := CityEstimate(es, w)
	if p1 != 30 || p1 != p2 || ci1 != ci2 {
		t.Errorf("estimate %d±%d vs %d±%d: must be 30 and reproducible", p1, ci1, p2, ci2)
	}
	if ci1 < 1 || ci1 > 10 {
		t.Errorf("ci = %d pp, expected a few points", ci1)
	}
}

func TestSurvey(t *testing.T) {
	if n, s := Survey(map[string]int{"fear_tax": 2, "other": 2}); n != 0 || s != nil {
		t.Error("4 answers must not be published")
	}
	n, s := Survey(map[string]int{"fear_tax": 3, "complex_docs": 1, "other": 1})
	if n != 5 || s[0].Answer != "fear_tax" || s[0].Pct != 60 {
		t.Errorf("survey = %d %+v", n, s)
	}
}

func TestSeedConfigOffsetsBalance(t *testing.T) {
	var w, sum float64
	for _, s := range seedSectors {
		w += s.share
		sum += s.share * s.offset
	}
	if w < 0.999 || w > 1.001 || sum > 0.005 || sum < -0.005 {
		t.Errorf("sector shares %.3f, weighted offset %.4f", w, sum)
	}
}
