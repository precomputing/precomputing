package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/internal/sqlite"
	"precomputing.com/precomputing/internal/version"
	"precomputing.com/precomputing/mcp"
)

const serveHelp = `Precomputing Engine

POST /v1/events?source=NAME[&stream=NAME]   events, one per line: JSON objects, or CSV with
     Content-Type: text/csv. The reply comes once they are in the file:
     {"applied": n, "skipped": n, "refused": [...], "committed": SEQ}
GET  /v1/answers/NAME                       the rows of a precompute, as JSON
GET  /v1/query?sql=SELECT...                a read-only query, as JSON
GET  /v1/stats                              counters and senders
POST /mcp                                   the Model Context Protocol, for AI agents: tools
     that describe the file, read its answers and windows, and fetch what it keeps whole

With a token file, every request needs "Authorization: Bearer TOKEN". A read token may
read and use /mcp; a write token may also post events.
`

func serveCmd(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	pol := fs.String("policy", "", "policy to make a new file from")
	addr := fs.String("addr", "localhost:8080", "address to listen on")
	every := fs.Duration("every", 100*time.Millisecond, "checkpoint at least this often")
	sync_ := fs.String("sync", "full", "full, normal or off: how hard each checkpoint waits for the disk")
	readOnly := fs.Bool("read-only", false, "only read the file: no events, no checkpoints")
	tokenFile := fs.String("token-file", "", "file of tokens, one per line: read TOKEN or write TOKEN")
	origins := fs.String("allow-origin", "", "comma-separated origins of web pages that may call /mcp (local pages always may)")
	rate := fs.Float64("rate", 20, "tool calls a second allowed on /mcp for each token or address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "precomputing serve: give exactly one file")
		return 2
	}
	path := fs.Arg(0)
	var tokens []token
	if *tokenFile != "" {
		var err error
		if tokens, err = readTokens(*tokenFile); err != nil {
			return fail("%v", err)
		}
	} else if !loopback(*addr) {
		log.Printf("precomputing: warning: no --token-file, so anyone who can reach %s can use this server", *addr)
	}
	var eng *engine.Engine
	if !*readOnly {
		var st interface{ Close() error }
		var err error
		eng, st, err = openEngine(path, *pol, *sync_)
		if err != nil {
			return fail("%v", err)
		}
		defer st.Close()
	} else if _, err := os.Stat(path); err != nil {
		return fail("%v", err)
	}
	ro, err := sqlite.OpenReadOnly(path)
	if err != nil {
		return fail("%v", err)
	}
	defer ro.Close()
	agentDB, err := sqlite.OpenReadOnly(path)
	if err != nil {
		return fail("%v", err)
	}
	defer agentDB.Close()
	agents := &mcp.Server{
		Reader: &mcp.SQLiteReader{DB: agentDB}, Name: "precomputing", Title: "Precomputing: " + filepath.Base(path),
		Version: version.Version, AllowOrigin: originPolicy(*origins),
	}

	var mu sync.Mutex // the Engine
	var roMu sync.Mutex
	done := sync.NewCond(&mu)
	generation := 0 // checkpoints finished
	var lastErr error
	parsers := map[string]*parser{}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	quit := make(chan struct{}) // closed once the server has stopped taking requests
	go func() {
		if eng == nil {
			return
		}
		t := time.NewTicker(*every)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
			}
			mu.Lock()
			if eng.Pending() > 0 {
				if err := eng.Checkpoint(); err != nil {
					lastErr = err
					log.Printf("checkpoint: %v", err)
				} else {
					lastErr = nil
				}
			}
			generation++
			done.Broadcast()
			mu.Unlock()
		}
	}()

	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, serveHelp) })
	mux.HandleFunc("POST /v1/events", func(w http.ResponseWriter, r *http.Request) {
		if eng == nil {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "this server only reads its file (--read-only)"})
			return
		}
		source := r.URL.Query().Get("source")
		if source == "" {
			source = "http"
		}
		format := "json"
		if strings.Contains(r.Header.Get("Content-Type"), "csv") {
			format = "csv"
		}
		stream := r.URL.Query().Get("stream")
		type refusal struct {
			Line  int    `json:"line"`
			Error string `json:"error"`
		}
		refused := []refusal{}
		mu.Lock()
		key := format + "|" + stream
		p := parsers[key]
		if p == nil {
			var err error
			if p, err = newParser(eng, format, stream, format == "csv" && r.URL.Query().Get("seq") != ""); err != nil {
				mu.Unlock()
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			parsers[key] = p
		}
		before := eng.Stats()
		sc := bufio.NewScanner(r.Body)
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		line := 0
		for sc.Scan() {
			line++
			b := sc.Bytes()
			if len(strings.TrimSpace(string(b))) == 0 {
				continue
			}
			ev, err := p.parse(b)
			if err == nil {
				err = eng.PutID(ev.stream, source, ev.seq, ev.ts, ev.id, ev.key, ev.vals)
			}
			if err != nil {
				var stopped *engine.StoppedError
				if errors.As(err, &stopped) {
					mu.Unlock()
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
					return
				}
				refused = append(refused, refusal{line, err.Error()})
			}
		}
		after := eng.Stats()
		// Wait for the next checkpoint: it holds every event applied above.
		start := generation
		for generation == start {
			done.Wait()
		}
		committed, ckErr := eng.Committed(source), lastErr
		mu.Unlock()
		if ckErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "checkpoint: " + ckErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"applied": after.Events - before.Events, "skipped": after.Duplicates - before.Duplicates,
			"refused": refused, "committed": committed,
		})
	})
	read := func(w http.ResponseWriter, sql string) {
		roMu.Lock()
		res, err := query(ro, sql)
		roMu.Unlock()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		rows := make([]map[string]any, len(res.Rows))
		for j, row := range res.Rows {
			rows[j] = map[string]any{}
			for i, c := range res.Columns {
				rows[j][c] = row[i]
			}
		}
		writeJSON(w, http.StatusOK, rows)
	}
	mux.HandleFunc("GET /v1/answers/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		roMu.Lock()
		res, err := query(ro, "SELECT 1 FROM _precomputing_objects WHERE name = ? AND kind = 'precompute'", name)
		roMu.Unlock()
		if err != nil || len(res.Rows) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no precompute %q", name)})
			return
		}
		read(w, "SELECT * FROM "+name)
	})
	mux.HandleFunc("GET /v1/query", func(w http.ResponseWriter, r *http.Request) { read(w, r.URL.Query().Get("sql")) })
	mux.HandleFunc("GET /v1/stats", func(w http.ResponseWriter, r *http.Request) {
		var s engine.Stats
		var pending int64
		if eng != nil {
			mu.Lock()
			s = eng.Stats()
			pending = eng.Pending()
			mu.Unlock()
		}
		roMu.Lock()
		src, _ := query(ro, "SELECT source, seq, events, newest FROM _precomputing_sources ORDER BY source")
		roMu.Unlock()
		var sources []map[string]any
		if src != nil {
			for _, row := range src.Rows {
				sources = append(sources, map[string]any{"source": row[0], "seq": row[1], "events": row[2], "newest": row[3]})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"events": s.Events, "skipped": s.Duplicates, "checkpoints": s.Checkpoints, "rows_written": s.RowsWritten,
			"pending": pending, "sources": sources,
		})
	})

	limiter := newLimiter(*rate)
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !limiter.allow(clientOf(r)) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many calls; try again in a second", http.StatusTooManyRequests)
			return
		}
		agents.ServeHTTP(w, r)
	}))

	srv := &http.Server{Addr: *addr, Handler: authorize(tokens, mux), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	mode := ""
	if eng == nil {
		mode = " (read only)"
	}
	log.Printf("precomputing: serving %s%s on %s; agents connect to http://%s/mcp", path, mode, *addr, hostPort(*addr))
	select {
	case err := <-errc:
		return fail("%v", err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
	close(quit)
	if eng == nil {
		return 0
	}
	mu.Lock()
	defer mu.Unlock()
	if err := eng.Checkpoint(); err != nil {
		return fail("final checkpoint: %v", err)
	}
	log.Printf("precomputing: stopped; every event received is in %s", path)
	return 0
}
