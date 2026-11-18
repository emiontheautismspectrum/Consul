// Package consul is the chat assistant. Code computes every number (ledger, calculators);
// the LLM only understands the question and explains tool results. Replies use the
// frontend's Reply format, every number in them is checked against tool results, and a
// template answers whenever the LLM is unavailable, slow or not grounded.
package consul

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"finadvisor/internal/ledger/parse"
)

type Card struct {
	Title string      `json:"title"`
	Value string      `json:"value"`
	Rows  [][2]string `json:"rows"`
}

type Action struct {
	Label   string `json:"label"`
	Href    string `json:"href,omitempty"`
	Cx      string `json:"cx,omitempty"`
	Run     string `json:"run,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// Work is one line of "how it was computed": [verified, description].
type Work struct {
	OK   bool
	Text string
}

func (w Work) MarshalJSON() ([]byte, error) { return json.Marshal([2]any{w.OK, w.Text}) }

func (w *Work) UnmarshalJSON(b []byte) error {
	var raw [2]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[0], &w.OK); err != nil {
		return err
	}
	return json.Unmarshal(raw[1], &w.Text)
}

type Reply struct {
	Text      string       `json:"text"`
	Card      *Card        `json:"card"`
	Actions   []Action     `json:"actions"`
	Ask       *string      `json:"ask"`
	Work      []Work       `json:"work"`
	Sug       []string     `json:"sug"`
	Fallback  bool         `json:"fallback"`
	Topic     string       `json:"topic"`
	PendingOp *parse.Draft `json:"pending_op"`
	Rent      *parse.Rent  `json:"rent,omitempty"`
	ChatID    string       `json:"chat_id"`
}

func (r *Reply) normalize() {
	if r.Actions == nil {
		r.Actions = []Action{}
	}
	if r.Work == nil {
		r.Work = []Work{}
	}
	if r.Sug == nil {
		r.Sug = []string{}
	}
	if r.Topic == "" {
		r.Topic = "general"
	}
}

// Allowed links and commands. Anything else in a model answer is dropped, so a prompt
// injection cannot put an external link or an unknown command in front of the user.
var (
	allowedHref = map[string]bool{"/app": true, "/app/pul": true, "/app/ops": true, "/app/nasiya": true, "/app/consul": true,
		"/app/kredit": true, "/app/soliq": true, "/app/reja": true, "/app/agar": true, "/app/hisob": true, "/app/oq": true,
		"/app/men": true, "/kalkulyatorlar": true}
	allowedCx = map[string]bool{"kredit": true, "soliq": true, "foyda": true, "narx": true}
	// Names the frontend handles (components/cabinet/Reply.tsx ActionLink).
	allowedRun = map[string]bool{"why": true, "bankReport": true, "planWizard": true, "planReport": true,
		"confirm_op": true, "edit_op": true, "add_rent": true}
	topics = map[string]bool{"loan": true, "tax": true, "profit": true, "debtors": true, "spend": true, "plan": true, "price": true, "general": true}
	urlRe  = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
)

// sanitize enforces the allowlists and limits on a model reply.
func (r *Reply) sanitize() {
	r.Text = strings.TrimSpace(urlRe.ReplaceAllString(r.Text, ""))
	kept := r.Actions[:0]
	for _, a := range r.Actions {
		if (a.Href == "" || allowedHref[a.Href]) && (a.Cx == "" || allowedCx[a.Cx]) && (a.Run == "" || allowedRun[a.Run]) &&
			(a.Href != "" || a.Run != "" || a.Cx != "") && strings.TrimSpace(a.Label) != "" {
			a.Label = truncate(urlRe.ReplaceAllString(a.Label, ""), 60)
			kept = append(kept, a)
		}
		if len(kept) == 3 {
			break
		}
	}
	r.Actions = kept
	if len(r.Sug) > 4 {
		r.Sug = r.Sug[:4]
	}
	for i := range r.Sug {
		r.Sug[i] = truncate(urlRe.ReplaceAllString(r.Sug[i], ""), 60)
	}
	if !topics[r.Topic] {
		r.Topic = "general"
	}
	r.PendingOp = nil // only draft_operation (code) sets it, never the model's JSON
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// --- grounding: every number shown must come from a tool ---

// A unit counts only as a whole word: "21 kun" (days) is not 21 000 ("21 k").
var numRe = regexp.MustCompile(`(\d{1,3}(?:[ \x{00a0}]\d{3})+|\d+(?:[.,]\d+)?)\s*(?:(mlrd|milliard|млрд|mln|million|млн|ming|минг|тыс\p{L}*|k)(?:\P{L}|$))?`)

func mult(u string) float64 {
	switch {
	case u == "mlrd" || u == "milliard" || u == "млрд":
		return 1e9
	case u == "mln" || u == "million" || u == "млн":
		return 1e6
	case u == "ming" || u == "минг" || u == "k" || strings.HasPrefix(u, "тыс"):
		return 1e3
	}
	return 1
}

// numbersIn extracts the numbers a person reads in the text ("1,8 mln" = 1 800 000).
// labelRe matches a letter followed by digits ("D1", "A4"): a label, not an amount.
var labelRe = regexp.MustCompile(`\p{L}\d+`)

func numbersIn(s string) []float64 {
	var out []float64
	// Labels are removed first: in "D1 500 000" the amount is 500 000, not 1 500 000.
	for _, m := range numRe.FindAllStringSubmatch(labelRe.ReplaceAllString(strings.ToLower(s), " "), -1) {
		n := strings.NewReplacer(" ", "", "\u00a0", "").Replace(m[1])
		f, err := strconv.ParseFloat(strings.ReplaceAll(n, ",", "."), 64)
		if err != nil {
			continue
		}
		out = append(out, f*mult(m[2]))
	}
	return out
}

// grounded reports whether v is a tool value as people round it: exact, ±0.5%, rounded to
// thousands ("~1 798 000" for 1 797 617) or to 0.1 mln ("1,8 mln").
func grounded(v float64, pool []float64) bool {
	for _, p := range pool {
		switch {
		case v == p, math.Abs(v-p) <= math.Max(math.Abs(p)*0.005, 0.5):
			return true
		case v >= 1000 && math.Round(p/1000) == math.Round(v/1000):
			return true
		case v >= 100_000 && math.Abs(v-p) <= 50_000 && math.Abs(p) >= 1_000_000:
			return true
		}
	}
	return false
}

// unverified returns the numbers in the reply that no tool produced.
func unverified(r Reply, pool []float64) []float64 {
	texts := []string{r.Text}
	if r.Card != nil {
		texts = append(texts, r.Card.Title, r.Card.Value)
		for _, row := range r.Card.Rows {
			texts = append(texts, row[1])
		}
	}
	var bad []float64
	for _, t := range texts {
		for _, v := range numbersIn(t) {
			if !grounded(v, pool) {
				bad = append(bad, v)
			}
		}
	}
	return bad
}

// collect gathers every number in a JSON document, including digits inside strings
// (dates such as "2026-12-15" give 2026, 12 and 15).
func collect(v any, out *[]float64) {
	switch x := v.(type) {
	case float64:
		*out = append(*out, x)
	case string:
		for _, f := range strings.FieldsFunc(x, func(r rune) bool { return r < '0' || r > '9' }) {
			if n, err := strconv.ParseFloat(f, 64); err == nil {
				*out = append(*out, n)
			}
		}
	case map[string]any:
		for _, e := range x {
			collect(e, out)
		}
	case []any:
		for _, e := range x {
			collect(e, out)
		}
	}
}

// fmtSom renders 4200000 as "4 200 000".
func fmtSom(v int64) string {
	s := strconv.FormatInt(v, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func round1000(v int64) int64 { return int64(math.Round(float64(v)/1000)) * 1000 }
