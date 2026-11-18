package palata

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"finadvisor/internal/infrastructure/httpx"
)

// Official comparison data: a different measure (share of GDP, not of trade turnover),
// labelled as such on the page.
var official = map[string]any{
	"value_pct":    21.8,
	"services_pct": 23.8,
	"period":       "2026-01/2026-09",
	"source":       "https://anhor.uz/uzl/iqtisodiyot/unobserved-economy-uzbekistan",
	"note":         "Unobserved economy as a share of GDP — a different measure from the share of unrecorded trade turnover.",
}

type Handler struct{ repo *Repo }

func NewHandler(r *Repo) *Handler { return &Handler{repo: r} }

// Register mounts the public, read-only report (aggregates only).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /palata/report", h.json)
	mux.HandleFunc("GET /palata/report.csv", h.csv)
}

type report struct {
	Window    int              `json:"window"`
	PeriodEnd string           `json:"period_end"`
	Synthetic bool             `json:"synthetic"`
	KPI       map[string]any   `json:"kpi"`
	Trend     []map[string]any `json:"trend"`
	Sectors   []Group          `json:"sectors"`
	Markets   []Group          `json:"markets"`
	Survey    map[string]any   `json:"survey"`
	Official  map[string]any   `json:"official"`
}

func (h *Handler) build(r *http.Request) (report, int, error) {
	window, _ := strconv.Atoi(r.URL.Query().Get("window"))
	if window == 0 {
		window = 30
	}
	if window != 30 && window != 90 {
		return report{}, http.StatusBadRequest, errors.New("window must be 30 or 90")
	}
	rows, err := h.repo.latest(r.Context(), window)
	if err != nil {
		return report{}, 0, err
	}
	rep := report{Window: window, Trend: []map[string]any{}, Sectors: []Group{}, Markets: []Group{}, Official: official,
		Survey: map[string]any{"n": 0, "answers": []SurveyShare{}}}
	for _, s := range rows {
		rep.PeriodEnd = s.Period.Format(time.DateOnly)
		rep.Synthetic = rep.Synthetic || s.Synthetic
		switch s.Cut {
		case "all":
			var m struct {
				Group  Group          `json:"group"`
				City   int            `json:"city_unrecorded_pct"`
				CI     int            `json:"ci_pp"`
				Survey map[string]any `json:"survey"`
			}
			if err := json.Unmarshal(s.Metrics, &m); err != nil {
				return report{}, 0, err
			}
			rep.KPI = map[string]any{"n": m.Group.N, "unrecorded_pct": m.City, "unrecorded_ci_pp": m.CI, "official_pct": m.Group.OfficialPct,
				"switched_to_official": m.Group.Switched, "median_turnover": m.Group.MedianTurnover}
			if m.Survey != nil {
				rep.Survey = m.Survey
			}
		case "sector", "market":
			var g Group
			if err := json.Unmarshal(s.Metrics, &g); err != nil {
				return report{}, 0, err
			}
			if s.Cut == "sector" {
				rep.Sectors = append(rep.Sectors, g)
			} else {
				rep.Markets = append(rep.Markets, g)
			}
		}
	}
	// The trend is stored once, with the 30-day window.
	if trend, err := h.repo.latest(r.Context(), 30); err == nil {
		for _, s := range trend {
			if s.Cut == "trend" {
				var m map[string]any
				if json.Unmarshal(s.Metrics, &m) == nil {
					rep.Trend = append(rep.Trend, m)
				}
				rep.Synthetic = rep.Synthetic || s.Synthetic
			}
		}
	}
	return rep, http.StatusOK, nil
}

func (h *Handler) respondErr(w http.ResponseWriter, r *http.Request, status int, err error) {
	switch {
	case status == http.StatusBadRequest:
		httpx.FieldError(w, status, "window_invalid", "window", err.Error())
	case errors.Is(err, ErrNoReport):
		httpx.Error(w, http.StatusNotFound, "no_report", "the report has not been computed yet")
	default:
		httpx.InternalError(w, r, err)
	}
}

func (h *Handler) json(w http.ResponseWriter, r *http.Request) {
	rep, status, err := h.build(r)
	if err != nil {
		h.respondErr(w, r, status, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rep)
}

// csv: ';' separator and a UTF-8 BOM so Excel opens Cyrillic and Uzbek letters correctly.
func (h *Handler) csv(w http.ResponseWriter, r *http.Request) {
	rep, status, err := h.build(r)
	if err != nil {
		h.respondErr(w, r, status, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="consul-palata-`+rep.PeriodEnd+`-`+strconv.Itoa(rep.Window)+`d.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	itoa := strconv.Itoa
	i64 := func(v int64) string { return strconv.FormatInt(v, 10) }
	num := func(v any) string {
		switch x := v.(type) {
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64)
		case int:
			return itoa(x)
		case int64:
			return i64(x)
		}
		return ""
	}
	_ = cw.Write([]string{"section", "key", "n", "unrecorded_pct", "official_pct", "median_turnover", "switched_to_official"})
	if k := rep.KPI; k != nil {
		_ = cw.Write([]string{"all", "all", num(k["n"]), num(k["unrecorded_pct"]), num(k["official_pct"]), num(k["median_turnover"]), num(k["switched_to_official"])})
	}
	for _, sec := range []struct {
		name string
		gs   []Group
	}{{"sector", rep.Sectors}, {"market", rep.Markets}} {
		for _, g := range sec.gs {
			_ = cw.Write([]string{sec.name, g.Key, itoa(g.N), itoa(g.UnrecordedPct), itoa(g.OfficialPct), i64(g.MedianTurnover), itoa(g.Switched)})
		}
	}
	for _, t := range rep.Trend {
		_ = cw.Write([]string{"trend", toStr(t["month"]), num(t["n"]), num(t["unrecorded_pct"]), "", "", ""})
	}
	if answers, ok := rep.Survey["answers"].([]any); ok {
		for _, a := range answers {
			if m, ok := a.(map[string]any); ok {
				_ = cw.Write([]string{"survey", toStr(m["answer"]), num(rep.Survey["n"]), num(m["pct"]), "", "", ""})
			}
		}
	}
	_ = cw.Write([]string{"official", "unobserved_economy_gdp_pct", "", num(rep.Official["value_pct"]), "", "", toStr(rep.Official["source"])})
	cw.Flush()
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
