package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMountVersionPrefix(t *testing.T) {
	v1 := http.NewServeMux()
	v1.HandleFunc("GET /plans/{id}", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(r.PathValue("id"))) })
	mux := http.NewServeMux()
	Mount(mux, APIv1, v1)

	cases := map[string]int{"/api/v1/plans/42": 200, "/plans/42": 404, "/api/v2/plans/42": 404}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
		if want == 200 && rec.Body.String() != "42" {
			t.Errorf("path value = %q", rec.Body)
		}
	}
}

func TestCORS(t *testing.T) {
	h := CORS([]string{"https://consul.env.uz"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	do := func(method, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/ops", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	// Preflight from the allowed origin: credentials, every method the API uses, Idempotency-Key.
	rec := do("OPTIONS", "https://consul.env.uz")
	hd := rec.Header()
	if rec.Code != 204 || hd.Get("Access-Control-Allow-Origin") != "https://consul.env.uz" || hd.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("preflight: %d %v", rec.Code, hd)
	}
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		if !strings.Contains(hd.Get("Access-Control-Allow-Methods"), m) {
			t.Errorf("method %s not allowed", m)
		}
	}
	if !strings.Contains(hd.Get("Access-Control-Allow-Headers"), "Idempotency-Key") || !strings.Contains(hd.Get("Access-Control-Expose-Headers"), "Retry-After") {
		t.Errorf("headers: %q / %q", hd.Get("Access-Control-Allow-Headers"), hd.Get("Access-Control-Expose-Headers"))
	}
	// Any other origin gets no CORS headers at all (and never "*" with credentials).
	for _, o := range []string{"https://evil.example", "https://consul.env.uz.evil.example", "http://consul.env.uz", "null"} {
		if got := do("GET", o).Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q was allowed: %q", o, got)
		}
	}
	if got := do("GET", "").Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("same-origin request got CORS headers: %q", got)
	}
}
