package vision

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"finadvisor/internal/infrastructure/httpx"
	"finadvisor/internal/infrastructure/llm"
)

// Photo uploads get a larger body limit than the rest of the API.
// Route is relative to the API version prefix (mounted under httpx.APIv1).
const Route = "/photo"

const disclaimer = "Распознавание выполнено ИИ и может содержать ошибки — сверяйте реквизиты и суммы с оригиналом. " +
	"Rasm sun'iy intellekt tomonidan o'qildi, xatolar bo'lishi mumkin. " +
	"Recognized by AI and may contain errors — verify details and amounts against the original."

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

func (h *Handler) Register(mux *http.ServeMux, reqAuth httpx.Middleware) {
	mux.Handle("POST "+Route, reqAuth(http.HandlerFunc(h.recognize)))
}

type response struct {
	Result
	Disclaimer string `json:"disclaimer"`
}

func (h *Handler) recognize(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxImageBytes+64<<10) // + multipart overhead
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "too_large", ErrTooLarge.Error())
			return
		}
		httpx.Error(w, http.StatusBadRequest, "bad_request", "expected multipart/form-data with a 'file' field")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, _, err := r.FormFile("file")
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", "field 'file' is required")
		return
	}
	defer f.Close()
	img, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}

	res, err := h.svc.Recognize(r.Context(), httpx.UserID(r.Context()), img, r.FormValue("lang"))
	switch {
	case errors.Is(err, ErrTooLarge):
		httpx.Error(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, ErrUnsupported):
		httpx.Error(w, http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error())
	case errors.Is(err, ErrConsentRequired):
		httpx.Error(w, http.StatusForbidden, "consent_required", err.Error())
	case errors.Is(err, ErrRateLimited):
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", err.Error())
	case errors.Is(err, ErrUnreadable):
		httpx.Error(w, http.StatusUnprocessableEntity, "unreadable", err.Error())
	case errors.Is(err, llm.ErrNotConfigured):
		httpx.Error(w, http.StatusServiceUnavailable, "llm_unavailable", err.Error())
	case errors.Is(err, llm.ErrUpstream):
		slog.ErrorContext(r.Context(), "vision upstream", "err", err, "request_id", httpx.RequestID(r.Context()))
		httpx.Error(w, http.StatusBadGateway, "llm_error", "AI provider error, please retry")
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		httpx.JSON(w, http.StatusOK, response{res, disclaimer})
	}
}
