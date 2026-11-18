package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"finadvisor/internal/calculator"
	"finadvisor/internal/infrastructure/llm"
)

// scriptedLLM replays canned assistant messages and records what it was sent.
type scriptedLLM struct {
	replies []llm.Message
	calls   [][]llm.Message
}

func (s *scriptedLLM) Model() string { return "fake" }

func (s *scriptedLLM) Chat(_ context.Context, req llm.Request) (llm.Message, llm.Usage, error) {
	s.calls = append(s.calls, append([]llm.Message(nil), req.Messages...))
	if len(s.calls) > len(s.replies) {
		return llm.Message{}, llm.Usage{}, errors.New("script exhausted")
	}
	return s.replies[len(s.calls)-1], llm.Usage{PromptTokens: 100, CompletionTokens: 50}, nil
}

func toolCall(id, name, args string) llm.ToolCall {
	tc := llm.ToolCall{ID: id, Type: "function"}
	tc.Function.Name, tc.Function.Arguments = name, args
	return tc
}

func engine(t *testing.T) calculator.Engine {
	r, err := calculator.LoadUZRules()
	if err != nil {
		t.Fatal(err)
	}
	return calculator.NewService(r, nil)
}

const finalJSON = `{"title":"Coffee point","summary":"ok",
 "sections":[{"key":"financing","heading":"Kredit","body":"..."}],
 "key_numbers":[{"label":"Monthly payment","value":9455959.66,"unit":"UZS","tool":"calculate_loan"},
                {"label":"Made up","value":123456,"unit":"UZS","tool":"calculate_loan"}],
 "assumptions":["a"],"risks":["r"],"next_steps":["n"]}`

func TestAgentUsesCalculatorsAndVerifiesNumbers(t *testing.T) {
	fake := &scriptedLLM{replies: []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			toolCall("1", "calculate_loan", `{"amount":100000000,"annual_rate":24,"term_months":12,"method":"annuity"}`),
		}},
		{Role: "assistant", Content: "```json\n" + finalJSON + "\n```"},
	}}
	res, err := NewAgent(fake, engine(t), 1000).Run(context.Background(), "uz", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trace) != 1 || res.Trace[0].Error != "" {
		t.Fatalf("trace = %+v", res.Trace)
	}
	// The tool result sent back to the model must contain the calculator's exact payment.
	toolMsg := fake.calls[1][len(fake.calls[1])-1]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "1" || !strings.Contains(toolMsg.Content, "9455959.66") {
		t.Errorf("tool message = %+v", toolMsg)
	}
	if strings.Contains(toolMsg.Content, `"schedule":[`) {
		t.Error("schedule should be stripped from the model context")
	}
	kn := res.Structured.KeyNumbers
	if !kn[0].Verified || kn[1].Verified {
		t.Errorf("grounding check wrong: %+v", kn)
	}
	if !strings.Contains(res.Markdown, "Asosiy ko'rsatkichlar") || !strings.Contains(res.Markdown, "9 455 959.66") {
		t.Errorf("markdown not localized/formatted:\n%s", res.Markdown)
	}
	if res.Usage.PromptTokens != 200 {
		t.Errorf("usage not accumulated: %+v", res.Usage)
	}
	if !strings.Contains(fake.calls[0][0].Content, "Uzbek") {
		t.Error("system prompt must request the user's language")
	}
}

func TestAgentForcesToolUse(t *testing.T) {
	fake := &scriptedLLM{replies: []llm.Message{
		{Role: "assistant", Content: finalJSON}, // tries to answer from its head
		{Role: "assistant", Content: finalJSON}, // ignores the nudge again
	}}
	_, err := NewAgent(fake, engine(t), 1000).Run(context.Background(), "ru", "plan")
	if !errors.Is(err, ErrNoToolUse) {
		t.Fatalf("err = %v, want ErrNoToolUse", err)
	}
	if last := fake.calls[1][len(fake.calls[1])-1]; last.Content != forceToolsMsg {
		t.Errorf("expected a nudge to use tools, got %+v", last)
	}
}

func TestAgentToolErrorsAreReturnedToModel(t *testing.T) {
	fake := &scriptedLLM{replies: []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			toolCall("1", "calculate_breakeven", `{"price_per_unit":100,"variable_cost_per_unit":200,"fixed_costs_monthly":1}`),
			toolCall("2", "rm_rf", `{}`),
			toolCall("3", "calculate_tax", `not json`),
		}},
		{Role: "assistant", Content: finalJSON},
	}}
	res, err := NewAgent(fake, engine(t), 1000).Run(context.Background(), "en", "plan")
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range res.Trace {
		if tr.Error == "" {
			t.Errorf("tool %s should have failed", tr.Name)
		}
	}
	if !json.Valid(mustJSON(t, res.Trace)) {
		t.Error("trace must be valid JSON even with bad arguments")
	}
}

func TestAgentRepairsInvalidJSON(t *testing.T) {
	fake := &scriptedLLM{replies: []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("1", "get_tax_rules", `{}`)}},
		{Role: "assistant", Content: "Here is your plan: it's great"},
		{Role: "assistant", Content: finalJSON},
	}}
	res, err := NewAgent(fake, engine(t), 1000).Run(context.Background(), "en", "plan")
	if err != nil || res.Structured == nil {
		t.Fatalf("repair failed: %v", err)
	}
}

func TestAgentIterationLimit(t *testing.T) {
	loop := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("1", "get_tax_rules", `{}`)}}
	replies := make([]llm.Message, maxIterations)
	for i := range replies {
		replies[i] = loop
	}
	_, err := NewAgent(&scriptedLLM{replies: replies}, engine(t), 1000).Run(context.Background(), "en", "plan")
	if err == nil || !strings.Contains(err.Error(), "no final answer") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnonymize(t *testing.T) {
	in := "Звоните +998 90 123-45-67 или 901234567, почта ali@mail.uz, СТИР 301234567, " +
		"ПИНФЛ 31234567890123, паспорт AA1234567, карта 8600 1234 5678 9012. Бюджет 500000000 сум."
	out := Anonymize(in)
	for _, leaked := range []string{"123-45-67", "901234567", "ali@mail.uz", "301234567", "31234567890123", "AA1234567", "8600 1234"} {
		if strings.Contains(out, leaked) {
			t.Errorf("leaked %q in %q", leaked, out)
		}
	}
	if !strings.Contains(out, "500000000") {
		t.Errorf("plain amounts must survive: %q", out)
	}
}

// --- service tests ---

type fakeConsent bool

func (f fakeConsent) HasConsent(context.Context, string) (bool, error) { return bool(f), nil }

type fakeLimiter struct{ n int }

func (f *fakeLimiter) Allow(_ context.Context, _ string, limit int, _ time.Duration) (bool, int, error) {
	f.n++
	return f.n <= limit, limit - f.n, nil
}

type memPlans struct{ saved []Plan }

func (m *memPlans) Save(_ context.Context, _ string, p *Plan) error {
	p.ID = "p1"
	m.saved = append(m.saved, *p)
	return nil
}
func (m *memPlans) List(context.Context, string, int, int) ([]Plan, int, error) { return nil, 0, nil }
func (m *memPlans) Get(context.Context, string, string) (Plan, error)           { return Plan{}, ErrNotFound }
func (m *memPlans) Delete(context.Context, string, string) error                { return nil }

func TestServiceRequiresConsent(t *testing.T) {
	s := NewService(NewAgent(&scriptedLLM{}, engine(t), 100), "fake", &memPlans{}, fakeConsent(false), nil, 0)
	_, err := s.Generate(context.Background(), "u", GenerateInput{Niche: "cafe", Budget: 1})
	if !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceAnonymizesAndSaves(t *testing.T) {
	fake := &scriptedLLM{replies: []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("1", "get_tax_rules", `{}`)}},
		{Role: "assistant", Content: finalJSON},
	}}
	repo := &memPlans{}
	lim := &fakeLimiter{}
	s := NewService(NewAgent(fake, engine(t), 100), "fake", repo, fakeConsent(true), lim, 1)
	in := GenerateInput{Lang: "xx", Niche: "Кофейня, тел +998901234567", Budget: 1e8}
	p, err := s.Generate(context.Background(), "u", in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Lang != "uz" || p.Title != "Coffee point" || len(repo.saved) != 1 {
		t.Errorf("plan = %+v", p)
	}
	for _, m := range fake.calls[0] {
		if strings.Contains(m.Content, "901234567") {
			t.Error("phone number leaked to the LLM")
		}
	}
	if strings.Contains(string(repo.saved[0].Input), "901234567") {
		t.Error("phone number persisted")
	}
	if _, err := s.Generate(context.Background(), "u", in); !errors.Is(err, ErrRateLimited) {
		t.Errorf("second call err = %v, want rate limited", err)
	}
}

func TestServiceValidation(t *testing.T) {
	s := NewService(NewAgent(&scriptedLLM{}, engine(t), 100), "fake", &memPlans{}, fakeConsent(true), nil, 0)
	for _, in := range []GenerateInput{
		{Budget: 1},
		{Niche: "x", Budget: 0},
		{Niche: "x", Budget: 1, EntityType: "corp"},
		{Niche: "x", Budget: 1, Loan: &LoanRequest{Amount: 0, TermMonths: 1}},
	} {
		if _, err := s.Generate(context.Background(), "u", in); !errors.Is(err, ErrValidation) {
			t.Errorf("Generate(%+v) err = %v", in, err)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
