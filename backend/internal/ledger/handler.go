package ledger

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"finadvisor/internal/infrastructure/httpx"
	"finadvisor/internal/ledger/parse"
)

//go:embed locales
var locales embed.FS

// Cursor is the opaque pagination token for GET /ops (occurred_at + id of the last row).
type Cursor struct {
	OccurredAt time.Time
	ID         string
}

func (c Cursor) Encode() string {
	raw := c.OccurredAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func DecodeCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, err
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return Cursor{}, errors.New("malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Cursor{}, err
	}
	if !validID(id) {
		return Cursor{}, errors.New("malformed cursor id")
	}
	return Cursor{OccurredAt: t, ID: id}, nil
}

type Handler struct {
	repo   *PGRepo
	policy TaxPolicy
	now    func() time.Time
}

func NewHandler(repo *PGRepo, p TaxPolicy) *Handler {
	return &Handler{repo: repo, policy: p, now: time.Now}
}

// Register mounts ledger routes; every one requires authentication.
func (h *Handler) Register(mux *http.ServeMux, reqAuth httpx.Middleware) {
	mux.Handle("GET /ledger/today", reqAuth(http.HandlerFunc(h.today)))
	mux.Handle("POST /ops/parse", reqAuth(http.HandlerFunc(h.parseText)))
	mux.Handle("GET /reports/bank", reqAuth(http.HandlerFunc(h.bankReport)))
	mux.Handle("POST /forecast/whatif", reqAuth(http.HandlerFunc(h.whatIf)))
	mux.Handle("POST /advisor/plan", reqAuth(http.HandlerFunc(h.plan)))
	mux.Handle("GET /me/export.csv", reqAuth(http.HandlerFunc(h.exportCSV)))
	mux.Handle("GET /ops", reqAuth(http.HandlerFunc(h.listOps)))
	mux.Handle("POST /ops", reqAuth(http.HandlerFunc(h.createOp)))
	mux.Handle("DELETE /ops/{id}", reqAuth(http.HandlerFunc(h.deleteOp)))
	mux.Handle("GET /debtors", reqAuth(http.HandlerFunc(h.debtors)))
	mux.Handle("POST /debtors/{id}/repaid", reqAuth(http.HandlerFunc(h.repaid)))
	mux.Handle("GET /debtors/{id}/reminder", reqAuth(http.HandlerFunc(h.reminder)))
	mux.Handle("GET /obligations", reqAuth(http.HandlerFunc(h.listObligations)))
	mux.Handle("POST /obligations", reqAuth(http.HandlerFunc(h.createObligation)))
	mux.Handle("PATCH /obligations/{id}", reqAuth(http.HandlerFunc(h.updateObligation)))
	mux.Handle("DELETE /obligations/{id}", reqAuth(http.HandlerFunc(h.deleteObligation)))
}

// Input limits. maxAmount (10 trillion so'm) keeps every sum of the journal far from int64
// overflow and rejects typos like an extra six zeros long before they reach the numbers.
const (
	maxAmount     int64 = 10_000_000_000_000
	maxNameRunes        = 100
	maxLabelRunes       = 200
	maxRawRunes         = 500
	maxFutureSkew       = 24 * time.Hour
)

var minOccurredAt = time.Date(2000, 1, 1, 0, 0, 0, 0, Tashkent)

// truncRunes cuts s to n characters. Cutting bytes could split a multi-byte letter (ш, ў)
// and PostgreSQL rejects invalid UTF-8.
func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// validID reports whether a path id is a UUID; anything else is treated as "not found"
// instead of reaching PostgreSQL as an invalid uuid (which would be a 500).
func validID(id string) bool { return uuid.Validate(id) == nil }

// pathID returns the {id} path value, or writes 404 "<what> not found" when it is not a UUID.
func pathID(w http.ResponseWriter, r *http.Request, what string) (string, bool) {
	id := r.PathValue("id")
	if !validID(id) {
		httpx.Error(w, http.StatusNotFound, "not_found", what+" not found")
		return "", false
	}
	return id, true
}

// checkCounterparty validates a customer/supplier name before it is stored.
func checkCounterparty(name string) error {
	if n := len([]rune(name)); n > maxNameRunes {
		return fieldErr{"counterparty_too_long", "counterparty", fmt.Sprintf("at most %d characters", maxNameRunes)}
	}
	if NameKey(name) == "" {
		return fieldErr{"counterparty_invalid", "counterparty", "the name must contain letters"}
	}
	return nil
}

// fieldErr is a validation failure mapped to the shared error envelope.
type fieldErr struct{ code, field, msg string }

func (e fieldErr) Error() string { return e.code }

func (h *Handler) freeNow(ctx context.Context, userID string) (int64, error) {
	st, err := h.repo.LoadState(ctx, userID)
	if err != nil {
		return 0, err
	}
	return Compute(st, h.now(), h.policy).View.Free, nil
}

func (h *Handler) today(w http.ResponseWriter, r *http.Request) {
	st, err := h.repo.LoadState(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, Compute(st, h.now(), h.policy))
}

// parseText understands a typed or spoken phrase. It never writes: the frontend shows the
// draft and calls POST /ops after the user confirms.
func (h *Handler) parseText(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "text_required", "text", "text is empty")
		return
	}
	if len([]rune(text)) > maxRawRunes {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "text_too_long", "text", fmt.Sprintf("at most %d characters", maxRawRunes))
		return
	}
	known, err := h.repo.Counterparties(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, parse.Parse(text, known))
}

type opInput struct {
	Kind         string          `json:"kind"`
	Amount       int64           `json:"amount"`
	Method       string          `json:"method"`
	Label        string          `json:"label"`
	Counterparty string          `json:"counterparty"`
	Fiscal       bool            `json:"fiscal"`
	Source       string          `json:"source"`
	RawText      string          `json:"raw_text"`
	Items        json.RawMessage `json:"items"`
	OccurredAt   *time.Time      `json:"occurred_at"`
}

var opSources = map[string]bool{"text": true, "voice": true, "photo": true, "manual": true}

func validMethod(m string) bool { return m == MethodNaqd || m == MethodKarta || m == MethodNone }

// buildOp validates the input and returns the op to store. For fix, balance is the
// current balance of the op's method: the journal keeps the signed difference.
func buildOp(in opInput, now time.Time, balance int64) (Op, error) {
	if !opKinds[in.Kind] {
		return Op{}, fieldErr{"kind_invalid", "kind", "unknown operation kind"}
	}
	o := Op{
		Kind:   in.Kind,
		Method: in.Method,
		Label:  in.Label,
		Fiscal: in.Fiscal,
		Source: in.Source,
		Items:  in.Items,
	}
	o.Label = truncRunes(o.Label, maxLabelRunes)
	o.RawText = truncRunes(in.RawText, maxRawRunes)
	if in.OccurredAt != nil {
		o.OccurredAt = *in.OccurredAt
		if o.OccurredAt.After(now.Add(maxFutureSkew)) || o.OccurredAt.Before(minOccurredAt) {
			return Op{}, fieldErr{"occurred_at_invalid", "occurred_at", "the date must be between 2000 and tomorrow"}
		}
	} else {
		o.OccurredAt = now
	}
	if in.Amount > maxAmount {
		return Op{}, fieldErr{"amount_too_large", "amount", "amount is unrealistically large"}
	}
	switch {
	case in.Kind == KindFix:
		if in.Amount < 0 {
			return Op{}, fieldErr{"amount_required", "amount", "fix amount is the actual balance and cannot be negative"}
		}
		if !validMethod(o.Method) || o.Method == MethodNone {
			return Op{}, fieldErr{"method_required", "method", "fix needs the method whose balance is corrected"}
		}
		o.Amount = in.Amount - balance // the stored, signed difference
	default:
		if in.Amount <= 0 {
			return Op{}, fieldErr{"amount_required", "amount", "amount must be a positive number of so'm"}
		}
		o.Amount = in.Amount
	}
	if o.Method == "" {
		if in.Kind == KindNas {
			o.Method = MethodNone
		} else {
			o.Method = MethodNaqd
		}
	}
	if !validMethod(o.Method) {
		return Op{}, fieldErr{"method_invalid", "method", "method must be naqd, karta or none"}
	}
	if in.Kind == KindNas && o.Method != MethodNone {
		return Op{}, fieldErr{"method_invalid", "method", "nasiya does not move cash: method must be none"}
	}
	if o.Source == "" {
		o.Source = "manual"
	}
	if !opSources[o.Source] {
		return Op{}, fieldErr{"source_invalid", "source", "unknown source"}
	}
	if len(o.Items) > 0 && !json.Valid(o.Items) {
		return Op{}, fieldErr{"items_invalid", "items", "items must be valid JSON"}
	}
	name := strings.TrimSpace(in.Counterparty)
	needsCP := in.Kind == KindNas || in.Kind == KindRepaid
	if needsCP && name == "" {
		return Op{}, fieldErr{"counterparty_required", "counterparty", "who is the customer"}
	}
	if name != "" {
		if err := checkCounterparty(name); err != nil {
			return Op{}, err
		}
	}
	return o, nil
}

type opResult struct {
	Op         Op    `json:"op"`
	FreeBefore int64 `json:"free_before"`
	FreeAfter  int64 `json:"free_after"`
}

// counterpartyKind maps operation kinds to counterparty kinds.
func counterpartyKind(opKind string) string {
	switch opKind {
	case KindNas, KindRepaid:
		return "customer"
	case KindSup, KindBuy:
		return "supplier"
	}
	return ""
}

func (h *Handler) createOp(w http.ResponseWriter, r *http.Request) {
	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idemKey == "" {
		httpx.FieldError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key", "the Idempotency-Key header is required")
		return
	}
	if len(idemKey) > 100 {
		httpx.FieldError(w, http.StatusBadRequest, "idempotency_key_invalid", "Idempotency-Key", "the key is too long")
		return
	}
	var in opInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	userID := httpx.UserID(r.Context())

	st, err := h.repo.LoadState(r.Context(), userID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	freeBefore := Compute(st, h.now(), h.policy).View.Free
	balance := int64(0)
	if in.Kind == KindFix && validMethod(in.Method) {
		balance = Balance(st.Ops, in.Method)
	}
	op, err := buildOp(in, h.now(), balance)
	if err != nil {
		var fe fieldErr
		if errors.As(err, &fe) {
			httpx.FieldError(w, http.StatusUnprocessableEntity, fe.code, fe.field, fe.msg)
			return
		}
		httpx.InternalError(w, r, err)
		return
	}

	if name := strings.TrimSpace(in.Counterparty); name != "" {
		if ck := counterpartyKind(op.Kind); ck != "" {
			id, err := h.repo.FindOrCreateCounterparty(r.Context(), userID, ck, name)
			if err != nil {
				httpx.InternalError(w, r, err)
				return
			}
			op.CounterpartyID = &id
			op.Counterparty = &name
		}
	}

	if op.Kind == KindRepaid {
		var owed int64
		if d, ok := findDebtor(st, h.now(), *op.CounterpartyID); ok {
			owed = d.Owed
		}
		// Same rule as the frontend: a repayment is capped at the debt; no debt → 422.
		if owed <= 0 {
			httpx.FieldError(w, http.StatusUnprocessableEntity, "no_debt", "counterparty", "this customer owes nothing")
			return
		}
		op.Amount = min(op.Amount, owed)
	}

	op.ID = uuid.NewString() // before marshaling: the stored idempotent response must contain it

	// free_after is computed in memory: the new op joins the state, no second query.
	st.Ops = append(st.Ops, op)
	if op.CounterpartyID != nil {
		st.Counterparties[*op.CounterpartyID] = Counterparty{Name: in.Counterparty}
	}
	res := opResult{Op: op, FreeBefore: freeBefore, FreeAfter: Compute(st, h.now(), h.policy).View.Free}

	body, err := json.Marshal(res)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	err = h.repo.InsertOp(r.Context(), userID, &op, idemKey, body)
	var rep Replay
	switch {
	case err == nil:
		res.Op = op
		httpx.JSON(w, http.StatusCreated, res)
	case errors.As(err, &rep):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rep.Response)
	default:
		httpx.InternalError(w, r, err)
	}
}

func (h *Handler) deleteOp(w http.ResponseWriter, r *http.Request) {
	opID, ok := pathID(w, r, "operation")
	if !ok {
		return
	}
	userID := httpx.UserID(r.Context())
	freeBefore, err := h.freeNow(r.Context(), userID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := h.repo.SoftDeleteOp(r.Context(), userID, opID); errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", "operation not found")
		return
	} else if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	freeAfter, err := h.freeNow(r.Context(), userID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int64{"free_before": freeBefore, "free_after": freeAfter})
}

func (h *Handler) listOps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := OpFilter{Kind: q.Get("kind"), Limit: 50}
	if f.Kind != "" && !opKinds[f.Kind] {
		httpx.FieldError(w, http.StatusBadRequest, "kind_invalid", "kind", "unknown operation kind")
		return
	}
	if l, err := strconv.Atoi(q.Get("limit")); q.Get("limit") != "" {
		if err != nil || l < 1 || l > 200 {
			httpx.FieldError(w, http.StatusBadRequest, "limit_invalid", "limit", "limit must be 1..200")
			return
		}
		f.Limit = l
	}
	parseDay := func(name string) (time.Time, error) {
		s := q.Get(name)
		if s == "" {
			return time.Time{}, nil
		}
		t, err := time.ParseInLocation(time.DateOnly, s, Tashkent)
		if err != nil {
			return time.Time{}, fieldErr{"date_invalid", name, "expected YYYY-MM-DD"}
		}
		return t, nil
	}
	var err error
	if f.From, err = parseDay("from"); err != nil {
		var fe fieldErr
		errors.As(err, &fe)
		httpx.FieldError(w, http.StatusBadRequest, fe.code, fe.field, fe.msg)
		return
	}
	if f.To, err = parseDay("to"); err != nil {
		var fe fieldErr
		errors.As(err, &fe)
		httpx.FieldError(w, http.StatusBadRequest, fe.code, fe.field, fe.msg)
		return
	}
	if s := q.Get("cursor"); s != "" {
		c, err := DecodeCursor(s)
		if err != nil {
			httpx.FieldError(w, http.StatusBadRequest, "cursor_invalid", "cursor", "malformed cursor")
			return
		}
		f.CursorOccurredAt, f.CursorID, f.HasCursor = c.OccurredAt, c.ID, true
	}
	ops, next, err := h.repo.ListOps(r.Context(), httpx.UserID(r.Context()), f)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := map[string]any{"items": ops}
	if next != nil {
		out["next_cursor"] = next.Encode()
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) debtors(w http.ResponseWriter, r *http.Request) {
	st, err := h.repo.LoadState(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	ds := Debtors(st, h.now())
	var total int64
	for _, d := range ds {
		total += d.Owed
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": ds, "total": total})
}

// findDebtor locates the debtor by counterparty id in the current state.
func findDebtor(st State, now time.Time, id string) (Debtor, bool) {
	for _, d := range Debtors(st, now) {
		if d.ID == id {
			return d, true
		}
	}
	return Debtor{}, false
}

func (h *Handler) repaid(w http.ResponseWriter, r *http.Request) {
	userID := httpx.UserID(r.Context())
	cpID, ok := pathID(w, r, "debtor")
	if !ok {
		return
	}
	st, err := h.repo.LoadState(r.Context(), userID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	d, ok := findDebtor(st, h.now(), cpID)
	if !ok {
		httpx.Error(w, http.StatusNotFound, "not_found", "debtor not found")
		return
	}
	var in struct {
		Amount int64  `json:"amount"`
		Method string `json:"method"`
	}
	// The body is optional ("whole debt"); chunked requests have no ContentLength, so
	// decode whenever there is a body and treat an empty one as {}.
	if err := httpx.Decode(r, &in); err != nil && !errors.Is(err, io.EOF) {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if in.Amount == 0 {
		in.Amount = d.Owed
	}
	if in.Amount < 0 {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "amount_required", "amount", "amount must be positive")
		return
	}
	in.Amount = min(in.Amount, d.Owed) // capped at the debt, like the frontend
	if in.Method == "" {
		in.Method = MethodNaqd
	}
	if in.Method != MethodNaqd && in.Method != MethodKarta {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "method_invalid", "method", "repaid moves cash: naqd or karta")
		return
	}

	freeBefore := Compute(st, h.now(), h.policy).View.Free
	op := Op{ID: uuid.NewString(), Kind: KindRepaid, Amount: in.Amount, Method: in.Method,
		Label: d.Name, CounterpartyID: &cpID, Source: "manual", OccurredAt: h.now()}
	op.Counterparty = &d.Name
	st.Ops = append(st.Ops, op)
	res := opResult{Op: op, FreeBefore: freeBefore, FreeAfter: Compute(st, h.now(), h.policy).View.Free}
	body, err := json.Marshal(res)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	// Idempotency-Key is optional here (contract); reuse the same replay mechanism when given.
	err = h.repo.InsertOp(r.Context(), userID, &op, strings.TrimSpace(r.Header.Get("Idempotency-Key")), body)
	var rep Replay
	switch {
	case err == nil:
		res.Op = op
		httpx.JSON(w, http.StatusCreated, res)
	case errors.As(err, &rep):
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rep.Response)
	default:
		httpx.InternalError(w, r, err)
	}
}

func (h *Handler) reminder(w http.ResponseWriter, r *http.Request) {
	userID := httpx.UserID(r.Context())
	cpID, ok := pathID(w, r, "debtor")
	if !ok {
		return
	}
	st, err := h.repo.LoadState(r.Context(), userID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	d, ok := findDebtor(st, h.now(), cpID)
	if !ok {
		httpx.Error(w, http.StatusNotFound, "not_found", "debtor not found")
		return
	}
	lang, err := h.repo.UserLang(r.Context(), userID)
	if err != nil || (lang != "ru" && lang != "uz") {
		lang = "uz"
	}
	tmpl, err := locales.ReadFile("locales/reminder." + lang + ".txt")
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{
		"text": fmt.Sprintf(strings.TrimSpace(string(tmpl)), d.Name, formatSom(d.Owed), d.Days),
	})
}

// formatSom renders 1700000 as "1 700 000".
func formatSom(v int64) string {
	s := strconv.FormatInt(v, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
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

type obligationInput struct {
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	Amount       int64  `json:"amount"`
	DueDay       *int   `json:"due_day"`
	DueDate      string `json:"due_date"`
	Recurring    bool   `json:"recurring"`
	Counterparty string `json:"counterparty"`
}

var obligationKinds = map[string]bool{"rent": true, "supplier": true, "loan": true, "other": true}

// parseObligation validates the input. userID is needed to resolve the supplier name.
func (h *Handler) parseObligation(ctx context.Context, userID string, in obligationInput) (Obligation, error) {
	if !obligationKinds[in.Kind] {
		return Obligation{}, fieldErr{"kind_invalid", "kind", "rent, supplier, loan or other"}
	}
	if in.Amount <= 0 {
		return Obligation{}, fieldErr{"amount_required", "amount", "amount must be positive"}
	}
	if in.Amount > maxAmount {
		return Obligation{}, fieldErr{"amount_too_large", "amount", "amount is unrealistically large"}
	}
	if in.DueDay == nil && in.DueDate == "" {
		return Obligation{}, fieldErr{"due_required", "due_day", "monthly payments pass due_day, one-off payments pass due_date"}
	}
	ob := Obligation{Kind: in.Kind, Label: in.Label, Amount: in.Amount, Recurring: in.Recurring}
	ob.Label = truncRunes(ob.Label, maxLabelRunes)
	if in.DueDay != nil {
		if *in.DueDay < 1 || *in.DueDay > 31 {
			return Obligation{}, fieldErr{"due_invalid", "due_day", "1..31"}
		}
		ob.DueDay = in.DueDay
	}
	if in.DueDate != "" {
		t, err := time.ParseInLocation(time.DateOnly, in.DueDate, Tashkent)
		if err != nil {
			return Obligation{}, fieldErr{"due_invalid", "due_date", "expected YYYY-MM-DD"}
		}
		ob.DueDate = &t
	}
	if name := strings.TrimSpace(in.Counterparty); name != "" {
		if err := checkCounterparty(name); err != nil {
			return Obligation{}, err
		}
		id, err := h.repo.FindOrCreateCounterparty(ctx, userID, "supplier", name)
		if err != nil {
			return Obligation{}, err
		}
		ob.CounterpartyID = &id
		ob.Counterparty = &name
	}
	return ob, nil
}

type obligationOut struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Label        string  `json:"label"`
	Amount       int64   `json:"amount"`
	DueDay       *int    `json:"due_day,omitempty"`
	DueDate      *string `json:"due_date,omitempty"`
	Recurring    bool    `json:"recurring"`
	Counterparty *string `json:"counterparty,omitempty"`
	NextDue      string  `json:"next_due"`
}

func obligationJSON(ob Obligation, now time.Time) obligationOut {
	out := obligationOut{
		ID: ob.ID, Kind: ob.Kind, Label: ob.Label, Amount: ob.Amount,
		DueDay: ob.DueDay, Recurring: ob.Recurring, Counterparty: ob.Counterparty,
	}
	if ob.DueDate != nil {
		s := day(*ob.DueDate).Format(time.DateOnly)
		out.DueDate = &s
	}
	if due, ok := nextDue(ob, day(now)); ok {
		out.NextDue = due.Format(time.DateOnly)
	}
	return out
}

func (h *Handler) listObligations(w http.ResponseWriter, r *http.Request) {
	st, err := h.repo.LoadState(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	items := make([]obligationOut, 0, len(st.Obligations))
	for _, ob := range st.Obligations {
		items = append(items, obligationJSON(ob, h.now()))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createObligation(w http.ResponseWriter, r *http.Request) {
	h.writeObligation(w, r, "")
}

func (h *Handler) updateObligation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "obligation")
	if !ok {
		return
	}
	h.writeObligation(w, r, id)
}

func (h *Handler) writeObligation(w http.ResponseWriter, r *http.Request, id string) {
	var in obligationInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	userID := httpx.UserID(r.Context())
	ob, err := h.parseObligation(r.Context(), userID, in)
	if err != nil {
		var fe fieldErr
		if errors.As(err, &fe) {
			httpx.FieldError(w, http.StatusUnprocessableEntity, fe.code, fe.field, fe.msg)
			return
		}
		httpx.InternalError(w, r, err)
		return
	}
	status := http.StatusCreated
	if id == "" {
		if err := h.repo.InsertObligation(r.Context(), userID, &ob); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	} else {
		ob.ID = id
		if err := h.repo.UpdateObligation(r.Context(), userID, &ob); errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "not_found", "obligation not found")
			return
		} else if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		status = http.StatusOK
	}
	httpx.JSON(w, status, obligationJSON(ob, h.now()))
}

func (h *Handler) deleteObligation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "obligation")
	if !ok {
		return
	}
	err := h.repo.DeleteObligation(r.Context(), httpx.UserID(r.Context()), id)
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", "obligation not found")
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) bankReport(w http.ResponseWriter, r *http.Request) {
	months := 3
	if v := r.URL.Query().Get("months"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 12 {
			httpx.FieldError(w, http.StatusBadRequest, "months_invalid", "months", "months must be 1..12")
			return
		}
		months = n
	}
	st, err := h.repo.LoadState(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, Bank(st, h.now(), months, h.policy))
}

func (h *Handler) whatIf(w http.ResponseWriter, r *http.Request) {
	var in WhatIfInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if in.Amount < 0 || in.Amount > maxAmount || in.UpliftPct < 0 || in.UpliftPct > 500 || in.AnnualRate < 0 || in.AnnualRate > 200 ||
		in.TermMonths < 0 || in.TermMonths > 120 || len(in.Payments) > 12 {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "scenario_invalid", "amount", "scenario values out of range")
		return
	}
	for _, p := range in.Payments {
		if p.Amount > maxAmount || p.Amount < -maxAmount {
			httpx.FieldError(w, http.StatusUnprocessableEntity, "amount_too_large", "payments", "amount is unrealistically large")
			return
		}
	}
	st, err := h.repo.LoadState(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	res, code := WhatIf(st, h.now(), in, h.policy)
	if code != "" {
		httpx.FieldError(w, http.StatusUnprocessableEntity, code, "kind", "invalid scenario")
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) plan(w http.ResponseWriter, r *http.Request) {
	var in PlanInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	for field, v := range map[string]int64{"capital": in.Capital, "sales": in.Sales, "rent": in.Rent, "helper": in.Helper} {
		if v < 0 || v > maxAmount {
			httpx.FieldError(w, http.StatusUnprocessableEntity, "amount_invalid", field, "must be 0.."+strconv.FormatInt(maxAmount, 10))
			return
		}
	}
	if in.Capital == 0 {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "amount_required", "capital", "capital is required")
		return
	}
	if in.CogsPct != nil && (*in.CogsPct < 0 || *in.CogsPct > 100) {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "cogs_invalid", "cogs_pct", "0..100")
		return
	}
	st, err := h.repo.LoadState(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, Plan(st, h.now(), in))
}

// exportCSV downloads all of the user's operations (';' separator, UTF-8 BOM for Excel).
func (h *Handler) exportCSV(w http.ResponseWriter, r *http.Request) {
	st, err := h.repo.LoadState(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="consul-operations-`+h.now().In(Tashkent).Format(time.DateOnly)+`.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write([]string{"date", "kind", "amount", "method", "label", "counterparty", "fiscal", "source"})
	for i := len(st.Ops) - 1; i >= 0; i-- { // newest first
		o := st.Ops[i]
		who := ""
		if o.Counterparty != nil {
			who = *o.Counterparty
		}
		_ = cw.Write([]string{csvSafe(o.OccurredAt.In(Tashkent).Format("2006-01-02 15:04")), o.Kind, strconv.FormatInt(o.Amount, 10), o.Method,
			csvSafe(o.Label), csvSafe(who), strconv.FormatBool(o.Fiscal), o.Source})
	}
	cw.Flush()
}

// csvSafe stops spreadsheet formula injection: a cell that starts with = + - @ is read as a
// formula by Excel, so user-typed text gets a leading apostrophe.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
