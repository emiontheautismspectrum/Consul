package httpx

import (
	"net/http"
	"net/http/httptest"
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
