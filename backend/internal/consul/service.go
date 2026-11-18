package consul

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"finadvisor/internal/calculator"
	"finadvisor/internal/infrastructure/llm"
	"finadvisor/internal/infrastructure/pii"
	"finadvisor/internal/ledger"
	"finadvisor/internal/ledger/parse"
)

var (
	ErrValidation   = errors.New("validation")
	ErrChatNotFound = errors.New("chat not found")
)

const (
	llmDeadline   = 8 * time.Second // default; a reply slower than this is replaced by a template
	maxSteps      = 6
	dailyLLMLimit = 200
	maxTextRunes  = 1000
)

type LedgerStore interface {
	LoadState(ctx context.Context, userID string) (ledger.State, error)
	Counterparties(ctx context.Context, userID string) ([]parse.Known, error)
	UserLang(ctx context.Context, userID string) (string, error)
}

// Meta is stored with the assistant message: real AI cost and why a template answered.
type Meta struct {
	Model     string
	TokensIn  int
	TokensOut int
	LatencyMS int
	Fallback  string // "" when the LLM answered
	Trace     []ToolTrace
}

type ChatStore interface {
	// EnsureChat returns chatID if it belongs to the user (ErrChatNotFound otherwise), or a new chat.
	EnsureChat(ctx context.Context, userID, chatID, title, topic string) (string, error)
	SaveTurn(ctx context.Context, chatID, userID, userText string, reply Reply, meta Meta) error
}

type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error)
}

type Service struct {
	llm      llm.Client
	engine   calculator.Engine
	policy   ledger.TaxPolicy
	ledger   LedgerStore
	chats    ChatStore
	limiter  Limiter
	tpls     map[string]*tpl
	prompts  map[string]string
	now      func() time.Time
	deadline time.Duration
}

func NewService(c llm.Client, e calculator.Engine, p ledger.TaxPolicy, l LedgerStore, chats ChatStore, lim Limiter) (*Service, error) {
	s := &Service{llm: c, engine: e, policy: p, ledger: l, chats: chats, limiter: lim,
		tpls: map[string]*tpl{}, prompts: map[string]string{}, now: time.Now, deadline: llmDeadline}
	for _, lang := range []string{"uz", "ru"} {
		t, err := loadTemplates(lang)
		if err != nil {
			return nil, err
		}
		s.tpls[lang] = t
		b, err := locales.ReadFile("locales/system." + lang + ".txt")
		if err != nil {
			return nil, err
		}
		s.prompts[lang] = string(b)
	}
	return s, nil
}

type AskInput struct {
	Text   string  `json:"text"`
	ChatID *string `json:"chat_id"`
	Route  string  `json:"route"`
}

// language: Cyrillic → ru, Latin letters → uz, no letters (just "500 000") → profile.
func (s *Service) language(ctx context.Context, userID, text string) string {
	if strings.IndexFunc(text, unicode.IsLetter) < 0 {
		if l, err := s.ledger.UserLang(ctx, userID); err == nil && (l == "ru" || l == "uz") {
			return l
		}
		return "uz"
	}
	return parse.Lang(text)
}

func (s *Service) Ask(ctx context.Context, userID string, in AskInput) (Reply, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" || len([]rune(text)) > maxTextRunes {
		return Reply{}, fmt.Errorf("%w: text must be 1..%d characters", ErrValidation, maxTextRunes)
	}
	lang := s.language(ctx, userID, text)
	t := s.tpls[lang]
	masked := pii.Mask(text)

	st, err := s.ledger.LoadState(ctx, userID)
	if err != nil {
		return Reply{}, err
	}
	known, err := s.ledger.Counterparties(ctx, userID)
	if err != nil {
		return Reply{}, err
	}
	now := s.now()
	today := ledger.Compute(st, now, s.policy)
	intent := parse.Parse(text, known)

	var reply Reply
	meta := Meta{}
	start := time.Now()
	switch intent.Type {
	case parse.TypeOp, parse.TypeFix, parse.TypeRent, parse.TypeNeedAmount, parse.TypeNeedKind:
		// Bookkeeping phrases never need the LLM: instant, free, and Consul never writes.
		reply = t.opReply(intent)
		meta.Fallback = "parser"
	default:
		topic := intent.Topic
		if intent.Type == parse.TypeReport {
			topic = "report"
		}
		reply, meta = s.answer(ctx, userID, lang, masked, in.Route, topic, st, today, now)
	}
	meta.LatencyMS = int(time.Since(start).Milliseconds())
	reply.normalize()

	chatID := ""
	if in.ChatID != nil {
		chatID = *in.ChatID
	}
	id, err := s.chats.EnsureChat(ctx, userID, chatID, truncate(masked, 60), reply.Topic)
	if err != nil {
		return Reply{}, err
	}
	reply.ChatID = id
	if err := s.chats.SaveTurn(ctx, id, userID, masked, reply, meta); err != nil {
		slog.ErrorContext(ctx, "consul: save turn", "err", err)
	}
	return reply, nil
}

// answer tries the agent and falls back to the topic template with a logged reason.
func (s *Service) answer(ctx context.Context, userID, lang, text, route, topic string, st ledger.State, today ledger.Today, now time.Time) (Reply, Meta) {
	sess := newSession(ctx, s.engine, st, today, now)
	f := facts{today: today, debtors: sess.debtors, lang: lang}
	if topic == "loan" {
		f.loan, _ = safeLoan(ctx, s.engine, today.View.Profit3M, 24, 24)
	}
	template := func(reason string, meta Meta) (Reply, Meta) {
		r := s.tpls[lang].topicReply(topic, f)
		r.Fallback = true
		meta.Fallback = reason
		slog.InfoContext(ctx, "consul: template answer", "reason", reason, "topic", topic)
		return r, meta
	}
	if s.llm == nil {
		return template("llm_not_configured", Meta{})
	}
	if s.limiter != nil {
		ok, _, err := s.limiter.Allow(ctx, "consul:"+userID+":"+now.UTC().Format("2006-01-02"), dailyLLMLimit, 24*time.Hour)
		if err == nil && !ok {
			return template("daily_limit", Meta{})
		}
	}
	reply, meta, err := s.agent(ctx, lang, text, route, topic, sess)
	if err != nil {
		reason := "llm_error"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			reason = "timeout"
		case errors.Is(err, llm.ErrNotConfigured):
			reason = "llm_not_configured"
		case errors.Is(err, errInvalidReply):
			reason = "invalid_json"
		case errors.Is(err, errUnverified):
			reason = "unverified_number"
		}
		slog.WarnContext(ctx, "consul: llm answer rejected", "reason", reason, "err", err)
		return template(reason, meta)
	}
	return reply, meta
}

var (
	errInvalidReply = errors.New("model reply is not valid Reply JSON")
	errUnverified   = errors.New("reply contains numbers no tool produced")
)

type modelReply struct {
	Text    string   `json:"text"`
	Card    *Card    `json:"card"`
	Actions []Action `json:"actions"`
	Ask     *string  `json:"ask"`
	Sug     []string `json:"sug"`
	Topic   string   `json:"topic"`
}

// prefetch: tools whose result the topic certainly needs. Code runs them before the first
// LLM call and sends the results with the question, so the model answers in one round trip
// instead of two (the reasoning model needs ~3 s per call). The numbers are still tool
// output and part of the grounding pool.
var prefetch = map[string][]string{
	"spend":   {"get_today"},
	"profit":  {"get_today", "get_months"},
	"tax":     {"get_today"},
	"loan":    {"get_today", "safe_loan"},
	"debtors": {"list_debtors", "get_today"},
	"plan":    {"get_today", "get_months", "safe_loan"},
	"general": {"get_today"},
}

func (s *Service) agent(parent context.Context, lang, text, route, topic string, sess *session) (Reply, Meta, error) {
	ctx, cancel := context.WithTimeout(parent, s.deadline)
	defer cancel()
	sess.ctx = ctx
	meta := Meta{Model: s.llm.Model()}
	user := text
	if route != "" {
		user = "[screen: " + truncate(route, 40) + "]\n" + text
	}
	var data strings.Builder
	for _, name := range prefetch[topic] {
		fmt.Fprintf(&data, "%s: %s\n", name, sess.exec(name, "{}"))
	}
	if data.Len() > 0 {
		user += "\n\nDATA (tool results, already computed):\n" + data.String()
	}
	msgs := []llm.Message{{Role: "system", Content: s.prompts[lang]}, {Role: "user", Content: user}}
	temp := 0.2
	for step := 0; step < maxSteps; step++ {
		msg, usage, err := s.llm.Chat(ctx, llm.Request{Messages: msgs, Tools: toolDefs, MaxTokens: 4000, Temperature: &temp})
		meta.TokensIn += usage.PromptTokens
		meta.TokensOut += usage.CompletionTokens
		meta.Trace = sess.trace
		if err != nil {
			if ctx.Err() != nil {
				return Reply{}, meta, context.DeadlineExceeded
			}
			return Reply{}, meta, err
		}
		msg.Role = "assistant"
		msgs = append(msgs, msg)
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				out := sess.exec(tc.Function.Name, tc.Function.Arguments)
				msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: out})
			}
			meta.Trace = sess.trace
			continue
		}
		var mr modelReply
		if err := json.Unmarshal([]byte(llm.ExtractJSON(msg.Content)), &mr); err != nil || strings.TrimSpace(mr.Text) == "" {
			return Reply{}, meta, errInvalidReply
		}
		r := Reply{Text: mr.Text, Card: mr.Card, Actions: mr.Actions, Ask: mr.Ask, Sug: mr.Sug, Topic: mr.Topic}
		r.normalize()
		r.sanitize()
		// Numbers the user typed may be repeated; everything else must come from a tool.
		// Domain constants are known facts, not money: the 3-month profit window, the
		// 30-day obligation horizon, the 30% payment cap, 12 months a year.
		pool := append(append(append([]float64{}, sess.pool...), numbersIn(text)...), 3, 12, 30)
		if bad := unverified(r, pool); len(bad) > 0 {
			return Reply{}, meta, fmt.Errorf("%w: %v", errUnverified, bad)
		}
		r.Text = sess.unmask(r.Text)
		if r.Card != nil {
			r.Card.Title, r.Card.Value = sess.unmask(r.Card.Title), sess.unmask(r.Card.Value)
			for i := range r.Card.Rows {
				r.Card.Rows[i] = [2]string{sess.unmask(r.Card.Rows[i][0]), sess.unmask(r.Card.Rows[i][1])}
			}
		}
		if r.Ask != nil {
			a := sess.unmask(*r.Ask)
			r.Ask = &a
		}
		seen := map[string]bool{}
		for _, tr := range sess.trace {
			if tr.Error == "" && !seen[tr.Name] {
				seen[tr.Name] = true
				r.Work = append(r.Work, Work{true, s.tpls[lang].s("tools", tr.Name)})
			}
		}
		r.PendingOp = sess.pending
		return r, meta, nil
	}
	return Reply{}, meta, fmt.Errorf("%w: no final answer after %d steps", errInvalidReply, maxSteps)
}
