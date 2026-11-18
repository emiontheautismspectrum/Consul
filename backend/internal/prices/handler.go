package prices

import (
	"net/http"
	"strconv"
	"strings"

	"finadvisor/internal/infrastructure/httpx"
)

type Handler struct{ repo *Repo }

func NewHandler(r *Repo) *Handler { return &Handler{repo: r} }

func (h *Handler) Register(mux *http.ServeMux, reqAuth httpx.Middleware) {
	mux.Handle("GET /prices", reqAuth(http.HandlerFunc(h.list)))
}

var sources = []map[string]string{
	{"id": "narx_stat", "title": "Milliy statistika qoʻmitasi — narx.stat.uz", "url": "https://narx.stat.uz", "note": "weekly average retail prices, republic and Tashkent city"},
	{"id": "user_op", "title": "Consul users (anonymous medians, at least 5 buyers)", "url": "", "note": "from receipts of users who agreed to anonymous statistics"},
	{"id": "seed", "title": "Sample data", "url": "", "note": "demo prices for dried fruit, not a measurement"},
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 60 {
		httpx.FieldError(w, http.StatusBadRequest, "q_invalid", "q", "at most 60 characters")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.repo.Lookup(r.Context(), httpx.UserID(r.Context()), q, limit)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	sample := false
	for _, it := range items {
		for _, m := range it.Markets {
			sample = sample || m.Sample
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "sources": sources, "sample": sample})
}
