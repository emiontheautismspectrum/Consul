package auth

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"finadvisor/internal/infrastructure/httpx"
	"finadvisor/internal/infrastructure/ratelimit"
)

type Handler struct {
	svc     *Service
	jwt     *JWTIssuer
	cookies CookieConfig

	// Phone login (optional until configured).
	otp     *OTP
	phones  PhoneRepo
	deleter Deleter
	profile func(ctx context.Context, userID string) (any, error)

	// Demo login: enabled only with APP_ENV=demo.
	demoPhone string
	demoReset func(ctx context.Context, userID string) error
}

// WithPhoneLogin enables POST /auth/otp/send and /auth/otp/verify. profile renders the
// user returned after login (the user module's profile).
func (h *Handler) WithPhoneLogin(o *OTP, phones PhoneRepo, profile func(context.Context, string) (any, error)) *Handler {
	h.otp, h.phones, h.profile = o, phones, profile
	return h
}

// WithAccountDeletion enables DELETE /me.
func (h *Handler) WithAccountDeletion(d Deleter) *Handler { h.deleter = d; return h }

// WithDemo enables POST /auth/demo for the given phone; reset restores its reference data.
func (h *Handler) WithDemo(phone string, reset func(context.Context, string) error) *Handler {
	h.demoPhone, h.demoReset = phone, reset
	return h
}

func NewHandler(svc *Service, j *JWTIssuer, c CookieConfig) *Handler {
	return &Handler{svc: svc, jwt: j, cookies: c}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/register", h.register)
	mux.HandleFunc("POST /auth/login", h.login)
	mux.HandleFunc("POST /auth/refresh", h.refresh)
	mux.HandleFunc("POST /auth/logout", h.logout)
	mux.HandleFunc("POST /auth/otp/send", h.otpSend)
	mux.HandleFunc("POST /auth/otp/verify", h.otpVerify)
	mux.HandleFunc("POST /auth/demo", h.demo)
	mux.Handle("DELETE /me", h.RequireAuth(http.HandlerFunc(h.deleteMe)))
}

// deleteMe erases the account and all its data. The caller must pass ?confirm=delete, so a
// stray request cannot do it; the refresh token is revoked and the cookies are cleared.
func (h *Handler) deleteMe(w http.ResponseWriter, r *http.Request) {
	if h.deleter == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "account deletion is not configured")
		return
	}
	if r.URL.Query().Get("confirm") != "delete" {
		httpx.FieldError(w, http.StatusBadRequest, "confirm_required", "confirm", "pass ?confirm=delete")
		return
	}
	if err := h.deleter.DeleteUser(r.Context(), httpx.UserID(r.Context())); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if c, err := r.Cookie(refreshCookie); err == nil {
		_ = h.svc.Logout(r.Context(), c.Value)
	}
	h.cookies.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) otpSend(w http.ResponseWriter, r *http.Request) {
	if h.otp == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "otp_unavailable", "phone login is not configured")
		return
	}
	var in struct {
		Phone string `json:"phone"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	phone, err := NormalizePhone(in.Phone)
	if err != nil {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "invalid_phone", "phone", "expected an Uzbek number: +998XXXXXXXXX")
		return
	}
	switch err := h.otp.Send(r.Context(), phone, ratelimit.ClientIP(r)); {
	case errors.Is(err, ErrOTPRateLimited):
		httpx.Error(w, http.StatusTooManyRequests, "otp_rate_limited", err.Error())
	case errors.Is(err, ErrOTPLocked):
		httpx.Error(w, http.StatusTooManyRequests, "otp_locked", err.Error())
	case err != nil:
		httpx.InternalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) otpVerify(w http.ResponseWriter, r *http.Request) {
	if h.otp == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "otp_unavailable", "phone login is not configured")
		return
	}
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
		Lang  string `json:"lang,omitempty"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	phone, err := NormalizePhone(in.Phone)
	if err != nil {
		httpx.FieldError(w, http.StatusUnprocessableEntity, "invalid_phone", "phone", "expected an Uzbek number: +998XXXXXXXXX")
		return
	}
	switch err := h.otp.Verify(r.Context(), phone, strings.TrimSpace(in.Code)); {
	case errors.Is(err, ErrOTPLocked):
		httpx.Error(w, http.StatusTooManyRequests, "otp_locked", err.Error())
		return
	case errors.Is(err, ErrInvalidCode):
		httpx.FieldError(w, http.StatusUnauthorized, "invalid_code", "code", err.Error())
		return
	case err != nil:
		httpx.InternalError(w, r, err)
		return
	}
	id, isNew, err := h.phones.FindOrCreateByPhone(r.Context(), phone, NormalizeLang(in.Lang))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.loggedIn(w, r, id, map[string]any{"is_new": isNew})
}

// demo logs into the demo account and resets its data. 404 outside APP_ENV=demo, so the
// route does not even reveal that it exists in production.
func (h *Handler) demo(w http.ResponseWriter, r *http.Request) {
	if h.demoReset == nil || h.phones == nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	id, _, err := h.phones.FindOrCreateByPhone(r.Context(), h.demoPhone, "uz")
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := h.demoReset(r.Context(), id); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.loggedIn(w, r, id, nil)
}

// loggedIn issues cookies and answers {user: Profile, ...extra}.
func (h *Handler) loggedIn(w http.ResponseWriter, r *http.Request, userID string, extra map[string]any) {
	t, err := h.jwt.Issue(userID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	prof, err := h.profile(r.Context(), userID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.cookies.SetTokens(w, t)
	out := map[string]any{"user": prof, "access_token": t.Access, "expires_in": int(h.jwt.accessTTL.Seconds())}
	for k, v := range extra {
		out[k] = v
	}
	httpx.JSON(w, http.StatusOK, out)
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
	var locked *LockedError
	switch {
	case errors.As(err, &locked):
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(locked.RetryIn.Seconds()))))
		httpx.Error(w, http.StatusTooManyRequests, "too_many_attempts",
			"too many failed login attempts for this account, try again later")
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
