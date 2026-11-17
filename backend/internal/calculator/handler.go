package calculator

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"finadvisor/internal/infrastructure/httpx"
)

const Disclaimer = "Расчёт носит информационный характер и не является налоговой, юридической или кредитной консультацией. " +
	"Hisob-kitob ma'lumot uchun berilgan va soliq yoki yuridik maslahat emas. " +
	"This calculation is for information only and is not tax, legal or credit advice."

type Handler struct {
	engine Engine
	repo   Repo
}

func NewHandler(e Engine, r Repo) *Handler { return &Handler{engine: e, repo: r} }

// Register mounts calculator routes. optAuth attaches the user (history is saved only for
// logged-in users); reqAuth guards the history endpoint.
func (h *Handler) Register(mux *http.ServeMux, optAuth, reqAuth httpx.Middleware) {
	mux.Handle("POST /api/v1/calculator/calculate", optAuth(http.HandlerFunc(h.loan)))
	mux.Handle("POST /api/v1/calculator/tax", optAuth(http.HandlerFunc(h.tax)))
	mux.Handle("POST /api/v1/calculator/breakeven", optAuth(http.HandlerFunc(h.breakeven)))
	mux.HandleFunc("GET /api/v1/calculator/rules", h.rules)
	mux.Handle("GET /api/v1/calculator/history", reqAuth(http.HandlerFunc(h.history)))
}

type envelope struct {
	Result     any      `json:"result"`
	Sources    []Source `json:"sources"`
	Disclaimer string   `json:"disclaimer"`
}

func (h *Handler) loan(w http.ResponseWriter, r *http.Request) {
	run(h, w, r, KindLoan, h.engine.Loan)
}

func (h *Handler) tax(w http.ResponseWriter, r *http.Request) {
	run(h, w, r, KindTax, h.engine.Tax)
}

func (h *Handler) breakeven(w http.ResponseWriter, r *http.Request) {
	run(h, w, r, KindBreakeven, h.engine.Breakeven)
}

func run[I, R any](h *Handler, w http.ResponseWriter, r *http.Request, kind string, calc func(context.Context, I) (R, error)) {
	var in I
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	res, err := calc(r.Context(), in)
	if errors.Is(err, ErrInvalidInput) {
		httpx.Error(w, http.StatusUnprocessableEntity, "validation", err.Error())
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if uid := httpx.UserID(r.Context()); uid != "" {
		if err := h.repo.Save(r.Context(), uid, kind, in, res); err != nil {
			slog.ErrorContext(r.Context(), "save calculation", "err", err)
		}
	}
	httpx.JSON(w, http.StatusOK, envelope{res, h.engine.Rules().Sources, Disclaimer})
}

func (h *Handler) rules(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, envelope{h.engine.Rules(), h.engine.Rules().Sources, Disclaimer})
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != KindLoan && kind != KindTax && kind != KindBreakeven {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "kind must be loan, tax or breakeven")
		return
	}
	limit, offset := httpx.Page(r, 10, 100)
	items, err := h.repo.History(r.Context(), httpx.UserID(r.Context()), kind, limit, offset)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}
