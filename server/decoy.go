package main

import (
	_ "embed"
	"net/http"
	"os"
)

//go:embed decoy.html
var defaultDecoyHTML []byte

// buildDecoyHandler returns the handler used for any request that is not a
// valid, authenticated tunnel handshake: a plain, boring static page that
// makes the origin look like an ordinary idle web server to anyone probing
// it (including the DPI box doing protocol classification).
func buildDecoyHandler(customPath string) http.Handler {
	body := defaultDecoyHTML
	if customPath != "" {
		if b, err := os.ReadFile(customPath); err == nil {
			body = b
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Server", "nginx")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})
}
