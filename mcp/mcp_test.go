//go:build cgo

package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/internal/sqlite"
)

// event is one latency event of the test file.
type event struct {
	ts int64
	ep string
	ms float64
}

// latencyFile compiles examples/latency.precompute into a file and inserts two hours of events
// through the compiled triggers. It returns a reader and the events.
func latencyFile(t *testing.T) (*SQLiteReader, []event) {
	t.Helper()
	text, err := os.ReadFile("../examples/latency.precompute")
	if err != nil {
		t.Fatal(err)
	}
	out, err := compile.Source(string(text), "latency.precompute")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "latency.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(out.Schema); err != nil {
		t.Fatal(err)
	}
	ins, err := db.Prepare("INSERT INTO latency (ts, endpoint, ms) VALUES (?, ?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	const start = 1790586000 // 2026-09-28 09:00 UTC
	var evs []event
	seed := uint32(7)
	rnd := func() float64 { // xorshift, so the test sees the same events every time
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		return float64(seed) / 4294967296
	}
	db.Exec("BEGIN")
	for s := int64(0); s < 7200; s++ {
		for k := 0; k < 3; k++ {
			ep := []string{"/api/search", "/api/checkout"}[k%2]
			ms := math.Round((20+rnd()*80)*100) / 100
			if ep == "/api/checkout" {
				ms *= 3
			}
			if s == 3000 && k == 0 {
				ms = 5000 // one slow request
			}
			ev := event{start + s, ep, ms}
			evs = append(evs, ev)
			if err := ins.Bind(1, ev.ts); err != nil {
				t.Fatal(err)
			}
			ins.Bind(2, ev.ep)
			ins.Bind(3, ev.ms)
			if _, err := ins.Step(); err != nil {
				t.Fatal(err)
			}
			ins.Reset()
		}
	}
	db.Exec("COMMIT")
	db.Close()
	ro, err := sqlite.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	return &SQLiteReader{DB: ro}, evs
}

// call runs a tool and fails the test on a tool error.
func call(t *testing.T, r Reader, name string, arguments string) string {
	t.Helper()
	var a map[string]any
	if err := json.Unmarshal([]byte(arguments), &a); err != nil {
		t.Fatal(err)
	}
	text, isErr := callTool(r, findTool(name), a)
	if isErr {
		t.Fatalf("%s %s: %s", name, arguments, text)
	}
	return text
}

// csvRows reads the CSV rows of a tool's text: a line that says what they are, then the header.
func csvRows(t *testing.T, text string) []map[string]string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) < 2 {
		return nil
	}
	head := strings.Split(lines[1], ",")
	var out []map[string]string
	for _, l := range lines[2:] {
		f := strings.Split(l, ",")
		if len(f) != len(head) {
			break // a note under the rows
		}
		m := map[string]string{}
		for i, h := range head {
			m[h] = f[i]
		}
		out = append(out, m)
	}
	return out
}

func near(a, b, rel float64) bool { return math.Abs(a-b) <= rel*math.Abs(b) }

func TestWindowsMatchTheEvents(t *testing.T) {
	r, evs := latencyFile(t)
	from, to := int64(1790589600), int64(1790593200) // 10:00 to 11:00
	var sel []float64
	var sum float64
	var first, last float64
	mn, mx := math.Inf(1), math.Inf(-1)
	for _, e := range evs {
		if e.ep == "/api/checkout" && e.ts >= from && e.ts < to {
			if len(sel) == 0 {
				first = e.ms
			}
			last = e.ms
			sel = append(sel, e.ms)
			sum += e.ms
			mn, mx = math.Min(mn, e.ms), math.Max(mx, e.ms)
		}
	}
	text := call(t, r, "get_windows", `{"stream":"latency","where":{"endpoint":"/api/checkout"},"from":"2026-09-28T10:00:00Z","to":"2026-09-28T11:00:00Z","every":"all"}`)
	if !strings.Contains(text, "one summary from 1 window of 1h") {
		t.Errorf("an aligned hour should read the 1h window:\n%s", text)
	}
	rows := csvRows(t, text)
	if len(rows) != 1 {
		t.Fatalf("rows: %v\n%s", rows, text)
	}
	row := rows[0]
	f := func(k string) float64 { v, _ := strconv.ParseFloat(row[k], 64); return v }
	if int(f("n")) != len(sel) {
		t.Errorf("n %v, want %d", row["n"], len(sel))
	}
	for _, c := range []struct {
		k    string
		want float64
	}{{"ms_avg", sum / float64(len(sel))}, {"ms_min", mn}, {"ms_max", mx}, {"ms_first", first}, {"ms_last", last}, {"ms_sum", sum}} {
		if !near(f(c.k), c.want, 1e-9) {
			t.Errorf("%s = %v, want %v", c.k, f(c.k), c.want)
		}
	}
	sort.Float64s(sel)
	for _, p := range []struct {
		k string
		q float64
	}{{"ms_p50", 0.5}, {"ms_p95", 0.95}, {"ms_p99", 0.99}} {
		exact := sel[int(math.Ceil(p.q*float64(len(sel))))-1]
		if !near(f(p.k), exact, 0.011) {
			t.Errorf("%s = %v, exact %v: more than 1%% apart", p.k, f(p.k), exact)
		}
	}

	// The same hour as 5-minute rows, added up from 10-second windows or 1-minute windows,
	// gives the same counts.
	text = call(t, r, "get_windows", `{"stream":"latency","where":{"endpoint":"/api/checkout"},"from":"2026-09-28T10:00:00Z","to":"2026-09-28T11:00:00Z","every":"5m","stats":["n","max"]}`)
	rows = csvRows(t, text)
	if len(rows) != 12 {
		t.Fatalf("12 rows of 5 minutes expected:\n%s", text)
	}
	total := 0
	for _, row := range rows {
		n, _ := strconv.Atoi(row["n"])
		total += n
	}
	if total != len(sel) {
		t.Errorf("5-minute rows add up to %d, want %d", total, len(sel))
	}

	// Every key added together.
	text = call(t, r, "get_windows", `{"stream":"latency","by":[],"from":"2026-09-28T09:00:00Z","to":"2026-09-28T11:00:00Z","every":"all","stats":["n"]}`)
	rows = csvRows(t, text)
	if len(rows) != 1 || rows[0]["n"] != strconv.Itoa(len(evs)) {
		t.Errorf("all keys together: %v, want n %d\n%s", rows, len(evs), text)
	}

	// An unaligned range reads only whole windows and says what they cover.
	text = call(t, r, "get_windows", `{"stream":"latency","where":{"endpoint":"/api/search"},"from":"2026-09-28T10:00:05Z","to":"2026-09-28T10:10:00Z","every":"all","stats":["n"]}`)
	if !strings.Contains(text, "The windows read cover 2026-09-28T10:00:10Z to 2026-09-28T10:10:00Z") {
		t.Errorf("unaligned range:\n%s", text)
	}
}

func TestAnswersKeptAndQuery(t *testing.T) {
	r, evs := latencyFile(t)
	text := call(t, r, "describe_file", `{}`)
	for _, want := range []string{"Stream latency", "p99_ms = p99(latency.ms) by endpoint, within 1%", "endpoint: /api/search, /api/checkout",
		fmt.Sprintf("%d events from 2026-09-28T09:00:00Z to 2026-09-28T10:59:59Z", len(evs))} {
		if !strings.Contains(text, want) {
			t.Errorf("describe_file lacks %q:\n%s", want, text)
		}
	}
	text = call(t, r, "get_answer", `{"name":"requests","where":{"endpoint":"/api/search"}}`)
	if rows := csvRows(t, text); len(rows) != 1 || rows[0]["value"] != strconv.Itoa(len(evs)/3*2) {
		t.Errorf("requests for /api/search: %v\n%s", rows, text)
	}
	text = call(t, r, "get_kept", `{"kind":"anomalies","stream":"latency","limit":5}`)
	if !strings.Contains(text, "5000") {
		t.Errorf("the slow request should be kept whole:\n%s", text)
	}
	text = call(t, r, "get_kept", `{"kind":"samples","stream":"latency","where":{"endpoint":"/api/search"},"from":"2026-09-28T10:00:00Z","to":"2026-09-28T10:01:00Z"}`)
	rows := csvRows(t, text)
	if len(rows) != 3 {
		t.Errorf("3 samples a minute: %v\n%s", rows, text)
	}
	for _, row := range rows {
		if !strings.HasPrefix(row["time"], "2026-09-28T10:00:") {
			t.Errorf("a sample outside the minute: %v", row)
		}
	}
	text = call(t, r, "query", `{"sql":"SELECT count(*) AS n FROM latency_raw","limit":5}`)
	if !strings.Contains(text, "n\n") {
		t.Errorf("query: %s", text)
	}
	for _, bad := range []string{`{"sql":"DELETE FROM latency_raw"}`, `{"sql":"SELECT 1; SELECT 2"}`, `{"sql":"INSERT INTO latency VALUES (1, 'x', 1)"}`, `{"sql":""}`} {
		var a map[string]any
		json.Unmarshal([]byte(bad), &a)
		if text, isErr := callTool(r, findTool("query"), a); !isErr {
			t.Errorf("%s was allowed: %s", bad, text)
		}
	}
	// Mistakes come back as tool errors a model can read.
	for _, c := range []struct{ tool, args, want string }{
		{"get_windows", `{"stream":"nope"}`, "its streams are latency"},
		{"get_windows", `{"stream":"latency","every":"7s"}`, "no kept windows fit every 7s"},
		{"get_windows", `{"stream":"latency","where":{"route":"/"}}`, "the columns are endpoint"},
		{"get_answer", `{"name":"p98_ms"}`, "it has requests, avg_ms, p99_ms"},
		{"get_kept", `{"kind":"templates"}`, "no templates"},
		{"get_windows", `{"stream":"latency","color":"red"}`, "does not take color"},
		{"get_windows", `{"stream":"latency","from":"yesterday"}`, "is not a time"},
	} {
		var a map[string]any
		json.Unmarshal([]byte(c.args), &a)
		text, isErr := callTool(r, findTool(c.tool), a)
		if !isErr || !strings.Contains(text, c.want) {
			t.Errorf("%s %s: %q (error %v), want %q", c.tool, c.args, text, isErr, c.want)
		}
	}
}

func TestSlowReadStops(t *testing.T) {
	r, _ := latencyFile(t)
	r.Timeout = 50 * time.Millisecond
	t0 := time.Now()
	_, err := r.Read("WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c", nil, 0)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("a read without end should be stopped: %v", err)
	}
	if time.Since(t0) > 2*time.Second {
		t.Errorf("it took %v to stop", time.Since(t0))
	}
	// The next read is not touched by the old timer.
	time.Sleep(80 * time.Millisecond)
	if _, err := r.Read("SELECT count(*) FROM latency_raw", nil, 0); err != nil {
		t.Errorf("the next read: %v", err)
	}
}

// headers makes the headers of a modern HTTP request.
func headers(kv ...string) Headers {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[strings.ToLower(kv[i])] = kv[i+1]
	}
	return func(name string) string { return m[strings.ToLower(name)] }
}

const meta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"}}`

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return m
}

func errCode(m map[string]any) int {
	e, _ := m["error"].(map[string]any)
	if e == nil {
		return 0
	}
	return int(e["code"].(float64))
}

func TestModernProtocol(t *testing.T) {
	r, _ := latencyFile(t)
	s := &Server{Reader: r, Name: "precomputing", Title: "Precomputing: test", Version: "test"}
	h := headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/list")
	status, body := s.Handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta+`}}`), h)
	m := decode(t, body)
	res, _ := m["result"].(map[string]any)
	if status != 200 || res == nil || res["resultType"] != "complete" || len(res["tools"].([]any)) != 5 {
		t.Fatalf("tools/list: %d %s", status, body)
	}
	info := res["_meta"].(map[string]any)["io.modelcontextprotocol/serverInfo"].(map[string]any)
	if info["name"] != "precomputing" {
		t.Errorf("serverInfo: %v", info)
	}
	status, body = s.Handle([]byte(`{"jsonrpc":"2.0","id":"d","method":"server/discover","params":{`+meta+`}}`), headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "server/discover"))
	if m := decode(t, body); status != 200 || m["id"] != "d" || !strings.Contains(string(body), `"supportedVersions":["2026-07-28","2025-11-25","2025-06-18","2025-03-26"]`) {
		t.Errorf("server/discover: %d %s", status, body)
	}
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_answer","arguments":{"name":"requests"},` + meta + `}}`
	status, body = s.Handle([]byte(call), headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "get_answer"))
	if m := decode(t, body); status != 200 || m["result"] == nil || !strings.Contains(string(body), "/api/search") {
		t.Errorf("tools/call: %d %s", status, body)
	}
	// The name header may come in base64.
	status, _ = s.Handle([]byte(call), headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "=?base64?"+base64.StdEncoding.EncodeToString([]byte("get_answer"))+"?="))
	if status != 200 {
		t.Errorf("a base64 Mcp-Name: %d", status)
	}
	for _, c := range []struct {
		body   string
		h      Headers
		status int
		code   int
	}{
		{call, headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "query"), 400, codeHeaderMismatch},
		{call, headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call"), 400, codeHeaderMismatch},
		{call, headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/list", "Mcp-Name", "get_answer"), 400, codeHeaderMismatch},
		{call, headers("Mcp-Method", "tools/call", "Mcp-Name", "get_answer"), 400, codeHeaderMismatch},
		{strings.Replace(call, "2026-07-28", "2099-01-01", 1), headers("MCP-Protocol-Version", "2099-01-01", "Mcp-Method", "tools/call", "Mcp-Name", "get_answer"), 400, codeUnsupportedVers},
		{`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`, headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/list"), 400, codeInvalidParams},
		{`{"jsonrpc":"2.0","id":4,"method":"prompts/list","params":{` + meta + `}}`, headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "prompts/list"), 404, codeMethodNotFound},
		{`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope",` + meta + `}}`, headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "nope"), 200, codeInvalidParams},
		{`{"jsonrpc":"2.0","id":6,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, headers("MCP-Protocol-Version", "2025-11-25", "Mcp-Method", "tools/list"), 400, codeHeaderMismatch},
		{`not json`, nil, 400, codeParse},
		{`{"jsonrpc":"2.0","id":null,"method":"ping"}`, nil, 400, codeInvalidRequest},
	} {
		status, body := s.Handle([]byte(c.body), c.h)
		if status != c.status || errCode(decode(t, body)) != c.code {
			t.Errorf("%s: %d %s, want %d and code %d", c.body, status, body, c.status, c.code)
		}
	}
	unsupported := decode(t, func() []byte {
		_, b := s.Handle([]byte(strings.Replace(call, "2026-07-28", "2099-01-01", 1)), headers("MCP-Protocol-Version", "2099-01-01", "Mcp-Method", "tools/call", "Mcp-Name", "get_answer"))
		return b
	}())
	data := unsupported["error"].(map[string]any)["data"].(map[string]any)
	if data["requested"] != "2099-01-01" || len(data["supported"].([]any)) != 4 {
		t.Errorf("unsupported version data: %v", data)
	}
	// A tool that fails on its arguments is a result with isError, not a protocol error.
	status, body = s.Handle([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_windows","arguments":{"stream":"x"},`+meta+`}}`),
		headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "tools/call", "Mcp-Name", "get_windows"))
	if m := decode(t, body); status != 200 || m["result"].(map[string]any)["isError"] != true {
		t.Errorf("tool error: %d %s", status, body)
	}
	// A notification is accepted without a body.
	if status, body := s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{`+meta+`}}`), headers("MCP-Protocol-Version", "2026-07-28", "Mcp-Method", "notifications/cancelled")); status != 202 || len(body) != 0 {
		t.Errorf("notification: %d %s", status, body)
	}
}

func TestLegacyProtocol(t *testing.T) {
	r, _ := latencyFile(t)
	s := &Server{Reader: r, Name: "precomputing", Version: "test"}
	for _, c := range []struct{ asked, agreed string }{{"2025-06-18", "2025-06-18"}, {"2025-03-26", "2025-03-26"}, {"2024-11-05", "2025-11-25"}, {"2026-07-28", "2025-11-25"}} {
		status, body := s.Handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+c.asked+`","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`), headers())
		m := decode(t, body)
		res := m["result"].(map[string]any)
		if status != 200 || res["protocolVersion"] != c.agreed || res["capabilities"].(map[string]any)["tools"] == nil {
			t.Errorf("initialize %s: %d %s", c.asked, status, body)
		}
		if _, has := res["resultType"]; has {
			t.Errorf("a legacy result carries no resultType: %s", body)
		}
	}
	if status, body := s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), headers("MCP-Protocol-Version", "2025-06-18")); status != 202 || len(body) != 0 {
		t.Errorf("initialized: %d %s", status, body)
	}
	status, body := s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"describe_file","arguments":{}}}`), headers("MCP-Protocol-Version", "2025-11-25"))
	if status != 200 || !strings.Contains(string(body), "Stream latency") {
		t.Errorf("legacy tools/call: %d %s", status, body)
	}
	if status, _ := s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`), headers("MCP-Protocol-Version", "1999-01-01")); status != 400 {
		t.Errorf("an unknown legacy version header: %d", status)
	}
	// 2025-03-26 clients may send batches.
	status, body = s.Handle([]byte(`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`), headers())
	var batch []map[string]any
	if err := json.Unmarshal(body, &batch); err != nil || status != 200 || len(batch) != 2 {
		t.Errorf("batch: %d %s", status, body)
	}
}

func TestTransports(t *testing.T) {
	r, _ := latencyFile(t)
	s := &Server{Reader: r, Name: "precomputing", Version: "test"}
	srv := httptest.NewServer(s)
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 405 {
		t.Errorf("GET: %d", res.StatusCode)
	}
	post := func(origin string) int {
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta+`}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/list")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := post(""); c != 200 {
		t.Errorf("POST: %d", c)
	}
	if c := post("http://localhost:5173"); c != 200 {
		t.Errorf("a local page: %d", c)
	}
	if c := post("https://evil.example"); c != 403 {
		t.Errorf("another origin: %d", c)
	}
	var out strings.Builder
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}

{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_answer","arguments":{"name":"requests"},` + meta + `}}
`)
	if err := s.ServeStdio(in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"protocolVersion":"2025-11-25"`) || !strings.Contains(lines[1], `"resultType":"complete"`) {
		t.Errorf("stdio:\n%s", out.String())
	}
}

func TestText(t *testing.T) {
	for _, c := range []struct {
		x    float64
		want string
	}{{0, "0"}, {12, "12"}, {-3, "-3"}, {0.5, "0.5"}, {187.41, "187.41"}, {34.21456123456789, "34.21456123"},
		{0.000123456789, "0.000123456789"}, {12193129.98, "12193129.98"}, {1e20, "100000000000000000000"}, {-0.25, "-0.25"}, {2.5e-12, "0.0000000000025"}} {
		if got := num(c.x); got != c.want {
			t.Errorf("num(%v) = %q, want %q", c.x, got, c.want)
		}
	}
	if got := digits(907.03125, 4); got != "907" {
		t.Errorf("digits 4: %q", got)
	}
	if got := digits(1587.944, 4); got != "1588" {
		t.Errorf("digits 4: %q", got)
	}
	for _, c := range []struct {
		in   any
		want int64
	}{{"2026-09-28T10:00:00Z", 1790589600}, {"2026-09-28T10:30:00-04:00", 1790605800}, {"2026-09-28T10:00", 1790589600}, {"2026-09-28 10:00:00", 1790589600},
		{"2026-09-28", 1790553600}, {float64(1790589600), 1790589600}, {"1790589600", 1790589600}, {"2026-09-28T10:00:00.5Z", 1790589600}} {
		got, err := parseTime(c.in)
		if err != nil || got != c.want {
			t.Errorf("parseTime(%v) = %d %v, want %d", c.in, got, err, c.want)
		}
	}
	for _, ok := range []string{"SELECT 1", "SELECT 1;", "SELECT ';' -- x;\n", "SELECT 1 /* ; */", `SELECT "a;b" FROM t;;`, "SELECT [x;y] FROM t"} {
		if err := singleStatement(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"SELECT 1; SELECT 2", "SELECT 1;DELETE FROM t", ";", "  ", "SELECT 'a'; 'b'"} {
		if singleStatement(bad) == nil {
			t.Errorf("%q passed", bad)
		}
	}
	var tb table
	tb.row("a,b", `say "hi"`, " x", "plain")
	if got := tb.String(); got != "\"a,b\",\"say \"\"hi\"\"\",\" x\",plain\n" {
		t.Errorf("csv: %q", got)
	}
}
