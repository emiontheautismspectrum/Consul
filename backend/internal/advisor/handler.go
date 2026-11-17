package advisor

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"finadvisor/internal/calculator"
	"finadvisor/internal/infrastructure/httpx"
	"finadvisor/internal/infrastructure/llm"
)

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

func (h *Handler) Register(mux *http.ServeMux, reqAuth httpx.Middleware) {
	mux.Handle("POST /api/v1/advisor/generate", reqAuth(http.HandlerFunc(h.generate)))
	mux.Handle("GET /api/v1/advisor/plans", reqAuth(http.HandlerFunc(h.list)))
	mux.Handle("GET /api/v1/advisor/plans/{id}", reqAuth(http.HandlerFunc(h.get)))
	mux.Handle("DELETE /api/v1/advisor/plans/{id}", reqAuth(http.HandlerFunc(h.delete)))
}

type planResponse struct {
	Plan
	Disclaimer string              `json:"disclaimer"`
	Sources    []calculator.Source `json:"sources"`
}

func (h *Handler) respond(w http.ResponseWriter, status int, p Plan) {
	httpx.JSON(w, status, planResponse{p, calculator.Disclaimer, h.svc.agent.engine.Rules().Sources})
}

func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	var in GenerateInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	p, err := h.svc.Generate(r.Context(), httpx.UserID(r.Context()), in)
	switch {
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusUnprocessableEntity, "validation", err.Error())
	case errors.Is(err, ErrConsentRequired):
		httpx.Error(w, http.StatusForbidden, "consent_required", err.Error())
	case errors.Is(err, ErrRateLimited):
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", err.Error())
	case errors.Is(err, llm.ErrNotConfigured):
		httpx.Error(w, http.StatusServiceUnavailable, "llm_unavailable", err.Error())
	case errors.Is(err, ErrNoToolUse):
		httpx.Error(w, http.StatusBadGateway, "llm_no_tools", "the model did not use calculators; please retry")
	case errors.Is(err, llm.ErrUpstream):
		httpx.Error(w, http.StatusBadGateway, "llm_error", "AI provider error, please retry")
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		h.respond(w, http.StatusCreated, p)
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	limit, offset := httpx.Page(r, 10, 50)
	plans, total, err := h.svc.List(r.Context(), httpx.UserID(r.Context()), limit, offset)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": plans, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if uuid.Validate(id) != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", ErrNotFound.Error())
		return
	}
	p, err := h.svc.Get(r.Context(), httpx.UserID(r.Context()), id)
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.respond(w, http.StatusOK, p)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if uuid.Validate(id) != nil {
		httpx.Error(w, http.StatusNotFound, "not_found", ErrNotFound.Error())
		return
	}
	err := h.svc.Delete(r.Context(), httpx.UserID(r.Context()), id)
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
