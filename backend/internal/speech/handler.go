package speech

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"finadvisor/internal/infrastructure/httpx"
)

// STTRoute is relative to the API version; it gets a larger body limit than the rest.
const STTRoute = "/voice/stt"

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

func (h *Handler) Register(mux *http.ServeMux, reqAuth httpx.Middleware) {
	mux.Handle("POST "+STTRoute, reqAuth(http.HandlerFunc(h.stt)))
	mux.Handle("POST /voice/tts", reqAuth(http.HandlerFunc(h.tts)))
}

// audioType sniffs the container (the browser's header is not trusted) and returns the
// MIME type VoiceLab expects, or "" when unsupported.
func audioType(b []byte) string {
	if bytes.HasPrefix(b, []byte("fLaC")) {
		return "audio/flac"
	}
	switch http.DetectContentType(b) {
	case "video/webm", "audio/webm":
		return "audio/webm"
	case "application/ogg", "audio/ogg":
		return "audio/ogg"
	case "audio/wave", "audio/wav", "audio/x-wav":
		return "audio/wav"
	case "audio/mpeg":
		return "audio/mpeg"
	case "video/mp4", "audio/mp4":
		return "audio/mp4"
	}
	return ""
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrUnavailable):
		// The UI shows the text input; details are already logged without audio or text.
		httpx.Error(w, http.StatusServiceUnavailable, "voice_unavailable", "voice is unavailable, please type instead")
	case errors.Is(err, ErrRateLimited):
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", err.Error())
	case errors.Is(err, ErrValidation):
		httpx.FieldError(w, http.StatusUnprocessableEntity, "text_invalid", "text", err.Error())
	default:
		httpx.InternalError(w, r, err)
	}
}

func (h *Handler) stt(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxAudioBytes+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpx.FieldError(w, http.StatusRequestEntityTooLarge, "audio_too_large", "audio", "audio must be at most 2 MB (~60 s)")
			return
		}
		httpx.Error(w, http.StatusBadRequest, "bad_request", "expected multipart/form-data with an 'audio' field")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, _, err := r.FormFile("audio")
	if err != nil {
		httpx.FieldError(w, http.StatusBadRequest, "audio_required", "audio", "field 'audio' is required")
		return
	}
	defer f.Close()
	audio, err := io.ReadAll(io.LimitReader(f, MaxAudioBytes+1))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if len(audio) > MaxAudioBytes {
		httpx.FieldError(w, http.StatusRequestEntityTooLarge, "audio_too_large", "audio", "audio must be at most 2 MB (~60 s)")
		return
	}
	mime := audioType(audio)
	if mime == "" {
		httpx.FieldError(w, http.StatusUnsupportedMediaType, "audio_unsupported", "audio", "use webm, ogg, mp4/m4a, wav, mp3 or flac")
		return
	}
	text, lang, err := h.svc.Transcribe(r.Context(), httpx.UserID(r.Context()), audio, mime, r.FormValue("lang"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"text": text, "lang": lang})
}

func (h *Handler) tts(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text  string  `json:"text"`
		Lang  string  `json:"lang"`
		Speed float64 `json:"speed"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	audio, ctype, err := h.svc.Speak(r.Context(), httpx.UserID(r.Context()), in.Text, in.Lang, in.Speed)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(audio)
}
