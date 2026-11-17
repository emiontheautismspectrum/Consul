package auth

import (
	"errors"
	"net/http"
	"strings"

	"finadvisor/internal/infrastructure/httpx"
)

type Handler struct {
	svc     *Service
	jwt     *JWTIssuer
	cookies CookieConfig
}

func NewHandler(svc *Service, j *JWTIssuer, c CookieConfig) *Handler {
	return &Handler{svc: svc, jwt: j, cookies: c}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/register", h.register)
	mux.HandleFunc("POST /auth/login", h.login)
	mux.HandleFunc("POST /auth/refresh", h.refresh)
	mux.HandleFunc("POST /auth/logout", h.logout)
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Lang     string `json:"lang,omitempty"`
}

type authResponse struct {
	UserID string `json:"user_id"`
	// Tokens are also returned in the body for API clients (Swagger, mobile).
	// Browsers should rely on the HttpOnly cookies only.
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, t, err := h.svc.Register(r.Context(), in.Email, in.Password, in.Lang)
	switch {
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusUnprocessableEntity, "validation", err.Error())
	case errors.Is(err, ErrEmailTaken):
		httpx.Error(w, http.StatusConflict, "email_taken", err.Error())
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		h.ok(w, http.StatusCreated, id, t)
	}
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, t, err := h.svc.Login(r.Context(), in.Email, in.Password)
	switch {
	case errors.Is(err, ErrBadCredentials):
		httpx.Error(w, http.StatusUnauthorized, "bad_credentials", err.Error())
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		h.ok(w, http.StatusOK, id, t)
	}
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(refreshCookie)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "no_refresh_token", "refresh token cookie missing")
		return
	}
	t, err := h.svc.Refresh(r.Context(), c.Value)
	switch {
	case errors.Is(err, ErrInvalidToken):
		h.cookies.Clear(w)
		httpx.Error(w, http.StatusUnauthorized, "invalid_token", "refresh token is invalid or revoked")
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		claims, _ := h.jwt.ParseAccess(t.Access)
		h.ok(w, http.StatusOK, claims.Subject, t)
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(refreshCookie); err == nil {
		if err := h.svc.Logout(r.Context(), c.Value); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	h.cookies.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ok(w http.ResponseWriter, status int, id string, t Tokens) {
	h.cookies.SetTokens(w, t)
	httpx.JSON(w, status, authResponse{UserID: id, AccessToken: t.Access, ExpiresIn: int(h.svc.jwt.accessTTL.Seconds())})
}

// RequireAuth accepts the access token from the HttpOnly cookie or, for API tools,
// an Authorization: Bearer header.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := h.userFromRequest(r); id != "" {
			next.ServeHTTP(w, r.WithContext(httpx.WithUserID(r.Context(), id)))
			return
		}
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "access token missing or expired")
	})
}

// OptionalAuth attaches the user id when a valid token is present, but never rejects.
func (h *Handler) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := h.userFromRequest(r); id != "" {
			r = r.WithContext(httpx.WithUserID(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) userFromRequest(r *http.Request) string {
	tok := ""
	if c, err := r.Cookie(accessCookie); err == nil {
		tok = c.Value
	} else if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		tok = strings.TrimPrefix(a, "Bearer ")
	}
	if tok == "" {
		return ""
	}
	c, err := h.jwt.ParseAccess(tok)
	if err != nil {
		return ""
	}
	return c.Subject
}
