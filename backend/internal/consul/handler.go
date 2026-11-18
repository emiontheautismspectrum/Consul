package consul

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"finadvisor/internal/infrastructure/httpx"
)

// Cache stores replies for a repeated Idempotency-Key (Redis).
type Cache interface {
	GetJSON(ctx context.Context, key string, dst any) (bool, error)
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
}

type Handler struct {
	svc   *Service
	cache Cache
}

func NewHandler(s *Service, c Cache) *Handler { return &Handler{svc: s, cache: c} }

func (h *Handler) Register(mux *http.ServeMux, reqAuth httpx.Middleware) {
	mux.Handle("POST /consul/ask", reqAuth(http.HandlerFunc(h.ask)))
}

const idemTTL = 10 * time.Minute

func (h *Handler) ask(w http.ResponseWriter, r *http.Request) {
	var in AskInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	userID := httpx.UserID(r.Context())
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > 100 {
		httpx.FieldError(w, http.StatusBadRequest, "idempotency_key_invalid", "Idempotency-Key", "the key is too long")
		return
	}
	cacheKey := "consul:idem:" + userID + ":" + key

	var reply Reply
	replayed := false
	if key != "" && h.cache != nil {
		if ok, err := h.cache.GetJSON(r.Context(), cacheKey, &reply); err == nil && ok {
			replayed = true
		}
	}
	if !replayed {
		var err error
		reply, err = h.svc.Ask(r.Context(), userID, in)
		switch {
		case errors.Is(err, ErrValidation):
			httpx.FieldError(w, http.StatusUnprocessableEntity, "text_invalid", "text", err.Error())
			return
		case errors.Is(err, ErrChatNotFound):
			httpx.Error(w, http.StatusNotFound, "not_found", "chat not found")
			return
		case err != nil:
			httpx.InternalError(w, r, err)
			return
		}
		if key != "" && h.cache != nil {
			_ = h.cache.SetJSON(r.Context(), cacheKey, reply, idemTTL)
		}
	}

	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		stream(w, reply)
		return
	}
	httpx.JSON(w, http.StatusOK, reply)
}

// stream sends the reply as Server-Sent Events: `delta` chunks of text, then one `reply`
// event with the whole Reply. The answer is validated before the first byte is sent, so a
// streamed number has already passed the grounding check.
func stream(w http.ResponseWriter, reply Reply) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	w.WriteHeader(http.StatusOK)
	words := strings.SplitAfter(reply.Text, " ")
	for i := 0; i < len(words); i += 4 {
		chunk := strings.Join(words[i:min(i+4, len(words))], "")
		b, _ := json.Marshal(map[string]string{"text": chunk})
		fmt.Fprintf(w, "event: delta\ndata: %s\n\n", b)
		_ = rc.Flush()
	}
	b, _ := json.Marshal(reply)
	fmt.Fprintf(w, "event: reply\ndata: %s\n\n", b)
	_ = rc.Flush()
}
