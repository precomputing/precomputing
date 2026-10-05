//go:build !js

package mcp

import (
	"io"
	"net/http"
	"strings"
)

// localOrigin reports whether an origin is a page served from this machine.
func localOrigin(origin string) bool {
	for _, p := range []string{"http://localhost", "https://localhost", "http://127.0.0.1", "https://127.0.0.1", "http://[::1]", "https://[::1]"} {
		if origin == p || strings.HasPrefix(origin, p+":") {
			return true
		}
	}
	return false
}

// ServeHTTP is the Streamable HTTP transport: one JSON-RPC message per POST, answered with one
// JSON object. There is no server-sent stream and no session, so GET and DELETE are refused.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		allow := s.AllowOrigin
		if allow == nil {
			allow = localOrigin
		}
		if !allow(origin) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write(replyError(nil, codeInvalidRequest, "this origin may not call the server", nil))
			return
		}
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "the MCP endpoint takes POST", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		http.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
		return
	}
	status, out := s.Handle(body, r.Header.Get)
	if len(out) > 0 {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	w.Write(out)
}

