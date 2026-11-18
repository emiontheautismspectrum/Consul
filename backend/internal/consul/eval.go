package consul

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"finadvisor/internal/calculator"
	"finadvisor/internal/infrastructure/llm"
	"finadvisor/internal/ledger"
	"finadvisor/internal/ledger/parse"
)

// Evaluation harness: runs a fixed question set against the real model on the reference
// demo ledger (free 4 200 000, tax 500 000, profit 6 000 000, debts 1 700 000, safe loan
// 34 mln) and checks what a person would check: no refusal, the right numbers, the right
// topic, drafts for "write it down", answered by the LLM rather than a template, and speed.
// Run it with `api eval-chat` (see Makefile eval-chat-vps) after changing a prompt or a model.

//go:embed locales/eval_questions.json
var evalQuestionsJSON []byte

type evalQuestion struct {
	Q       string  `json:"q"`
	Topic   string  `json:"topic"`
	Numbers []int64 `json:"numbers"`
	Source  bool    `json:"source"`  // must cite a source link (tax and support rules)
	Pending bool    `json:"pending"` // must return a draft operation
}

var refusalMarkers = []string{
	"yordam bera olmayman", "faqat pul", "rad etaman", "mening vazifam emas", "bunga javob bera olmayman",
	"не могу помочь", "только деньги", "не могу ответить", "не моя тема", "отказываюсь", "не могу с этим помочь",
}

type evalLedger struct{ now time.Time }

func (e evalLedger) LoadState(context.Context, string) (ledger.State, error) {
	return ledger.DemoSeed(e.now), nil
}
func (evalLedger) UserLang(context.Context, string) (string, error) { return "uz", nil }
func (e evalLedger) Counterparties(context.Context, string) ([]parse.Known, error) {
	st := ledger.DemoSeed(e.now)
	var out []parse.Known
	for _, c := range st.Counterparties {
		kind := "customer"
		if c.Name == "Akmal aka" {
			kind = "supplier"
		}
		out = append(out, parse.Known{Name: c.Name, Kind: kind})
	}
	return out, nil
}

type evalStore struct {
	mu    sync.Mutex
	last  Meta
	facts map[string]string
}

func (s *evalStore) EnsureChat(context.Context, string, string, string, string) (string, error) {
	return "eval", nil
}
func (s *evalStore) SaveTurn(_ context.Context, _, _, _ string, _ Reply, m Meta) error {
	s.mu.Lock()
	s.last = m
	s.mu.Unlock()
	return nil
}
func (s *evalStore) Facts(context.Context, string) ([]Fact, error)         { return nil, nil }
func (s *evalStore) PutFact(context.Context, string, string, string) error { return nil }

type EvalResult struct {
	Q        string
	OK       bool
	Problems []string
	How      string // llm, or the fallback reason
	Latency  time.Duration
	Answer   string
}

type EvalSummary struct {
	Total, Passed, ByLLM int
	P50, P95             time.Duration
	TokensIn, TokensOut  int
	Results              []EvalResult
}

// RunEval asks every question (optionally filtered by substring) and prints a report to w.
func RunEval(ctx context.Context, c llm.Client, e calculator.Engine, p ledger.TaxPolicy, deadline time.Duration, w io.Writer, only string) (EvalSummary, error) {
	var qs []evalQuestion
	if err := json.Unmarshal(evalQuestionsJSON, &qs); err != nil {
		return EvalSummary{}, err
	}
	now := time.Date(2026, time.November, 18, 12, 0, 0, 0, ledger.Tashkent)
	store := &evalStore{}
	svc, err := NewService(c, e, p, evalLedger{now}, store, nil, store)
	if err != nil {
		return EvalSummary{}, err
	}
	svc.now = func() time.Time { return now }
	svc.WithDeadline(deadline)
	if c != nil {
		fmt.Fprintf(w, "model: %s, deadline %s\n", c.Model(), svc.deadline)
	}

	var sum EvalSummary
	var lat []time.Duration
	for _, q := range qs {
		if only != "" && !strings.Contains(strings.ToLower(q.Q), strings.ToLower(only)) {
			continue
		}
		start := time.Now()
		r, err := svc.Ask(ctx, "eval-user", AskInput{Text: q.Q})
		res := EvalResult{Q: q.Q, Latency: time.Since(start), Answer: r.Text}
		if err != nil {
			res.Problems = append(res.Problems, "error: "+err.Error())
		}
		store.mu.Lock()
		meta := store.last
		store.mu.Unlock()
		res.How = "llm"
		if meta.Fallback != "" {
			res.How = meta.Fallback
		}
		sum.TokensIn += meta.TokensIn
		sum.TokensOut += meta.TokensOut

		all := r.Text
		if r.Card != nil {
			all += " " + r.Card.Title + " " + r.Card.Value
			for _, row := range r.Card.Rows {
				all += " " + row[1]
			}
		}
		low := strings.ToLower(r.Text)
		for _, m := range refusalMarkers {
			if strings.Contains(low, m) {
				res.Problems = append(res.Problems, "refusal: \""+m+"\"")
			}
		}
		for _, want := range q.Numbers {
			if !grounded(float64(want), numbersIn(all)) {
				res.Problems = append(res.Problems, fmt.Sprintf("number %s missing", fmtSom(want)))
			}
		}
		if q.Topic != "" && r.Topic != q.Topic {
			res.Problems = append(res.Problems, fmt.Sprintf("topic %q, want %q", r.Topic, q.Topic))
		}
		if q.Pending && r.PendingOp == nil {
			res.Problems = append(res.Problems, "no draft operation")
		}
		if q.Source && !hasSource(r) {
			res.Problems = append(res.Problems, "no knowledge-base source")
		}
		// A template is a correct fallback but not what we are measuring: record, do not fail,
		// except for questions that need a model (no numbers to compute, no parser).
		if res.How != "llm" && res.How != "parser" {
			res.Problems = append(res.Problems, "template: "+res.How)
		}
		res.OK = len(res.Problems) == 0
		if res.OK {
			sum.Passed++
		}
		if res.How == "llm" || res.How == "parser" {
			sum.ByLLM++
		}
		sum.Total++
		lat = append(lat, res.Latency)
		sum.Results = append(sum.Results, res)
		mark := "✓"
		if !res.OK {
			mark = "✗"
		}
		fmt.Fprintf(w, "%s %-46s %5.1fs  %-18s %s\n", mark, truncate(q.Q, 46), res.Latency.Seconds(), res.How, strings.Join(res.Problems, "; "))
		fmt.Fprintf(w, "    ↳ %s\n", truncate(strings.ReplaceAll(res.Answer, "\n", " "), 150))
	}
	if sum.Total == 0 {
		return sum, nil
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	sum.P50, sum.P95 = lat[len(lat)/2], lat[min(len(lat)-1, len(lat)*95/100)]
	fmt.Fprintf(w, "\npassed %d/%d (%.0f%%) · answered without a template %d/%d · p50 %.1fs p95 %.1fs · tokens in/out avg %d/%d\n",
		sum.Passed, sum.Total, 100*float64(sum.Passed)/float64(sum.Total), sum.ByLLM, sum.Total,
		sum.P50.Seconds(), sum.P95.Seconds(), sum.TokensIn/sum.Total, sum.TokensOut/sum.Total)
	_ = utf8.RuneCountInString
	return sum, nil
}

func hasSource(r Reply) bool {
	for _, w := range r.Work {
		if strings.Contains(w.Text, "http") {
			return true
		}
	}
	return false
}
