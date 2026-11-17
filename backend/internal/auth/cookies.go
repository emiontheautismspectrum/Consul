package auth

import (
	"net/http"
	"time"
)

const (
	accessCookie  = "access_token"
	refreshCookie = "refresh_token"
	refreshPath   = "/api/v1/auth"
)

type CookieConfig struct {
	Secure bool
	Domain string
}

func (c CookieConfig) set(w http.ResponseWriter, name, value, path string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, Domain: c.Domain,
		Expires: exp, MaxAge: int(time.Until(exp).Seconds()),
		HttpOnly: true, Secure: c.Secure, SameSite: http.SameSiteStrictMode,
	})
}

func (c CookieConfig) SetTokens(w http.ResponseWriter, t Tokens) {
	c.set(w, accessCookie, t.Access, "/", t.AccessExp)
	c.set(w, refreshCookie, t.Refresh, refreshPath, t.RefreshExp)
}

func (c CookieConfig) Clear(w http.ResponseWriter) {
	for name, path := range map[string]string{accessCookie: "/", refreshCookie: refreshPath} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, Domain: c.Domain, MaxAge: -1,
			HttpOnly: true, Secure: c.Secure, SameSite: http.SameSiteStrictMode})
	}
}
