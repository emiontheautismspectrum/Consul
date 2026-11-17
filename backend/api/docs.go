// Package api serves the OpenAPI contract and a Swagger UI for the frontend team.
package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var spec []byte

const swaggerHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>FinAdvisor API</title>
<link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/swagger-ui.min.css"></head>
<body><div id="ui"></div>
<script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.17.14/swagger-ui-bundle.min.js"></script>
<script>SwaggerUIBundle({url: "/api/v1/openapi.yaml", dom_id: "#ui", withCredentials: true});</script>
</body></html>`

// Register mounts GET /openapi.yaml and GET /docs (relative to the version prefix).
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(spec)
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerHTML))
	})
}
