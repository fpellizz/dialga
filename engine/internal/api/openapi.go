package api

import (
	_ "embed"
	"net/http"
)

// openAPISpec is the contract of this API. openapi_test.go keeps it in step
// with routes() and with the JSON of the Go types.
//
//go:embed openapi.yaml
var openAPISpec []byte

func (s *Server) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openAPISpec)
}
