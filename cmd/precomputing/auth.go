package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// A token lets a client in: read tokens may read the file and use /mcp; write tokens may also
// post events.
type token struct {
	scope string // read or write
	hash  [32]byte
	name  string
}

// readTokens reads a token file: one token a line, "read TOKEN" or "write TOKEN", optionally
// followed by a name for the logs. Lines starting with # are comments.
func readTokens(path string) ([]token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []token
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != "read" && fields[0] != "write") {
			return nil, fmt.Errorf("%s:%d: a line is \"read TOKEN\" or \"write TOKEN\", then an optional name", path, n)
		}
		if len(fields[1]) < 16 {
			return nil, fmt.Errorf("%s:%d: a token needs at least 16 characters", path, n)
		}
		t := token{scope: fields[0], hash: sha256.Sum256([]byte(fields[1]))}
		if len(fields) > 2 {
			t.name = strings.Join(fields[2:], " ")
		}
		out = append(out, t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no tokens", path)
	}
	return out, nil
}

// authorize lets a request through when it carries a token that allows it. With no tokens,
// everything is allowed.
func authorize(tokens []token, next http.Handler) http.Handler {
	if len(tokens) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var found *token
		if ok {
			h := sha256.Sum256([]byte(strings.TrimSpace(given)))
			for i := range tokens {
				// Comparing hashes in constant time says nothing about how close a guess came.
				if subtle.ConstantTimeCompare(h[:], tokens[i].hash[:]) == 1 {
					found = &tokens[i]
				}
			}
		}
		if found == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="precomputing"`)
			http.Error(w, "this server needs a token: Authorization: Bearer TOKEN", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/events") && found.scope != "write" {
			http.Error(w, "this token may read, not write", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientKey{}, fmt.Sprintf("%x", found.hash[:8]))))
	})
}

type clientKey struct{}

// clientOf names who is calling, for the rate limit: the token when there is one, else the address.
func clientOf(r *http.Request) string {
	if c, ok := r.Context().Value(clientKey{}).(string); ok {
		return c
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loopback reports whether an address to listen on stays on this machine.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hostPort is an address clients can use: ":8080" means localhost:8080.
func hostPort(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}

// originPolicy allows local pages and the origins given.
func originPolicy(list string) func(string) bool {
	allowed := map[string]bool{}
	for _, o := range strings.Split(list, ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed[strings.TrimSuffix(o, "/")] = true
		}
	}
	return func(origin string) bool {
		if allowed[origin] {
			return true
		}
		for _, p := range []string{"http://localhost", "https://localhost", "http://127.0.0.1", "https://127.0.0.1", "http://[::1]", "https://[::1]"} {
			if origin == p || strings.HasPrefix(origin, p+":") {
				return true
			}
		}
		return false
	}
}

// limiter allows each client rate calls a second, with bursts of twice that.
type limiter struct {
	mu      sync.Mutex
	rate    float64
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate float64) *limiter {
	return &limiter{rate: rate, buckets: map[string]*bucket{}}
}

func (l *limiter) allow(client string) bool {
	if l.rate <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[client]
	if b == nil {
		if len(l.buckets) > 10000 { // forget idle clients rather than grow without end
			for k, v := range l.buckets {
				if now.Sub(v.last) > time.Minute {
					delete(l.buckets, k)
				}
			}
		}
		b = &bucket{tokens: 2 * l.rate, last: now}
		l.buckets[client] = b
	}
	b.tokens = min(2*l.rate, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
