package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	os.WriteFile(path, []byte("# agents read, the gateway writes\nread  aaaaaaaaaaaaaaaaaaaa agent\nwrite bbbbbbbbbbbbbbbbbbbb gateway\n"), 0o600)
	tokens, err := readTokens(path)
	if err != nil || len(tokens) != 2 || tokens[0].name != "agent" {
		t.Fatalf("readTokens: %v %v", tokens, err)
	}
	for _, bad := range []string{"read short\n", "admin cccccccccccccccccccc\n", "# nothing\n"} {
		os.WriteFile(path, []byte(bad), 0o600)
		if _, err := readTokens(path); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	h := authorize(tokens, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(clientOf(r)))
	}))
	do := func(method, path, tok string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(""))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if c, _ := do("POST", "/mcp", ""); c != 401 {
		t.Errorf("no token: %d", c)
	}
	if c, _ := do("POST", "/mcp", "aaaaaaaaaaaaaaaaaaab"); c != 401 {
		t.Errorf("a wrong token: %d", c)
	}
	if c, who := do("POST", "/mcp", "aaaaaaaaaaaaaaaaaaaa"); c != 200 || len(who) != 16 {
		t.Errorf("a read token on /mcp: %d %q", c, who)
	}
	if c, _ := do("POST", "/v1/events?source=x", "aaaaaaaaaaaaaaaaaaaa"); c != 403 {
		t.Errorf("a read token posting events: %d", c)
	}
	if c, _ := do("POST", "/v1/events?source=x", "bbbbbbbbbbbbbbbbbbbb"); c != 200 {
		t.Errorf("a write token posting events: %d", c)
	}
	if c, _ := do("GET", "/v1/stats", "bbbbbbbbbbbbbbbbbbbb"); c != 200 {
		t.Errorf("a write token reading: %d", c)
	}
	// Without tokens, a client cannot pick its own name for the rate limit.
	open := authorize(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(clientOf(r))) }))
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("X-Precomputing-Client", "someone-else")
	rec := httptest.NewRecorder()
	open.ServeHTTP(rec, req)
	if rec.Body.String() == "someone-else" {
		t.Error("a header chose the rate-limit name")
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(5)
	ok := 0
	for i := 0; i < 20; i++ {
		if l.allow("a") {
			ok++
		}
	}
	if ok != 10 {
		t.Errorf("a burst of twice the rate: %d allowed", ok)
	}
	if !l.allow("b") {
		t.Error("another client has its own allowance")
	}
	time.Sleep(250 * time.Millisecond)
	if !l.allow("a") {
		t.Error("the allowance comes back with time")
	}
	if loopback(":8080") || !loopback("localhost:8080") || !loopback("127.0.0.1:1") || loopback("0.0.0.0:80") {
		t.Error("loopback")
	}
}
