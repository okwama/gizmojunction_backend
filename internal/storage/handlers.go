package storage

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Register wires the internal upload endpoint and the public document
// redirect directly onto the mux — plain net/http rather than huma, since
// binary request bodies and redirects don't fit huma's JSON-typed model
// well, and neither endpoint needs to appear in the generated OpenAPI
// client (the upload endpoint is only ever called by the Deno functions;
// the redirect is only ever dropped into an href/src, never fetched by app
// code).
func Register(mux *http.ServeMux, client *Client, internalSecret string) {
	mux.HandleFunc("POST /v1/internal/uploads", func(w http.ResponseWriter, r *http.Request) {
		if internalSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Secret")), []byte(internalSecret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		key := r.URL.Query().Get("key")
		if key == "" || strings.Contains(key, "..") {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}

		contentType := r.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20)) // 32MB cap
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if err := client.Upload(r.Context(), key, body, contentType); err != nil {
			http.Error(w, "upload failed: "+err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"key": key})
	})

	mux.HandleFunc("GET /v1/documents/{key...}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if key == "" {
			http.NotFound(w, r)
			return
		}

		url, err := client.PresignGET(r.Context(), key, 1*time.Hour)
		if err != nil {
			http.Error(w, "could not resolve document", http.StatusNotFound)
			return
		}

		http.Redirect(w, r, url, http.StatusFound)
	})
}
