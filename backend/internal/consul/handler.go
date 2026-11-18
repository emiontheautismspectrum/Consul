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
	repo  *PGRepo
}

func NewHandler(s *Service, c Cache, repo *PGRepo) *Handler {
	return &Handler{svc: s, cache: c, repo: repo}
}

func (h *Handler) Register(mux *http.ServeMux, reqAuth httpx.Middleware) {
	mux.Handle("POST /consul/ask", reqAuth(http.HandlerFunc(h.ask)))
	mux.Handle("GET /chats", reqAuth(http.HandlerFunc(h.chats)))
	mux.Handle("GET /chats/{id}/messages", reqAuth(http.HandlerFunc(h.messages)))
	mux.Handle("DELETE /chats/{id}", reqAuth(http.HandlerFunc(h.deleteChat)))
	mux.Handle("GET /memory", reqAuth(http.HandlerFunc(h.memory)))
	mux.Handle("PUT /memory", reqAuth(http.HandlerFunc(h.putMemory)))
	mux.Handle("DELETE /memory/{key}", reqAuth(http.HandlerFunc(h.deleteMemory)))
	mux.Handle("DELETE /memory", reqAuth(http.HandlerFunc(h.clearMemory)))
}

func (h *Handler) chats(w http.ResponseWriter, r *http.Request) {
	limit, _ := httpx.Page(r, 30, 100)
	items, err := h.repo.ListChats(r.Context(), httpx.UserID(r.Context()), limit)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) messages(w http.ResponseWriter, r *http.Request) {
	items, err := h.repo.ChatMessages(r.Context(), httpx.UserID(r.Context()), r.PathValue("id"))
	if errors.Is(err, ErrChatNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", "chat not found")
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) deleteChat(w http.ResponseWriter, r *http.Request) {
	err := h.repo.DeleteChat(r.Context(), httpx.UserID(r.Context()), r.PathValue("id"))
	if errors.Is(err, ErrChatNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", "chat not found")
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) memory(w http.ResponseWriter, r *http.Request) {
	items, err := h.repo.Facts(r.Context(), httpx.UserID(r.Context()))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

const (
	maxKeyRunes   = 60
	maxValueRunes = 300
)

func (h *Handler) putMemory(w http.ResponseWriter, r *http.Request) {
	var in struct{ Key, Value string }
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	in.Key, in.Value = strings.TrimSpace(in.Key), strings.TrimSpace(in.Value)
	switch {
	case in.Key == "" || len([]rune(in.Key)) > maxKeyRunes:
		httpx.FieldError(w, http.StatusUnprocessableEntity, "key_invalid", "key", "1..60 characters")
		return
	case in.Value == "" || len([]rune(in.Value)) > maxValueRunes:
		httpx.FieldError(w, http.StatusUnprocessableEntity, "value_invalid", "value", "1..300 characters")
		return
	}
	err := h.repo.PutFact(r.Context(), httpx.UserID(r.Context()), in.Key, in.Value)
	if errors.Is(err, ErrTooManyFacts) {
		httpx.Error(w, http.StatusUnprocessableEntity, "too_many_facts", "Consul remembers at most 50 facts; forget one first")
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.memory(w, r)
}

func (h *Handler) deleteMemory(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.DeleteFact(r.Context(), httpx.UserID(r.Context()), r.PathValue("key")); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) clearMemory(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.DeleteAllFacts(r.Context(), httpx.UserID(r.Context())); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
