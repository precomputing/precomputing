//go:build cgo

package traces

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/engine/sqlitestore"
	"precomputing.com/precomputing/policy"
)

// Fake AWS credentials for the masking tests. They are put together at run time so that secret
// scanners don't take this file for a leak; the tests see the same strings as before.
var (
	fakeAWSKeyID  = "AKIA" + "ABCDEFGHIJKLMNOP"
	fakeAWSSecret = "abcdEFGHijklMNOP" + "qrstUVWXyz0123456789/+AB"
)

func TestRedact(t *testing.T) {
	cases := []struct{ in, out string }{
		{`AWS_ACCESS_KEY_ID=` + fakeAWSKeyID + `\nAWS_SECRET_ACCESS_KEY=` + fakeAWSSecret + `\n`,
			`AWS_ACCESS_KEY_ID=[redacted aws-key-id]\nAWS_SECRET_ACCESS_KEY=[redacted aws-secret]\n`},
		{`aws_secret_access_key = \"` + fakeAWSSecret + `\"`, `aws_secret_access_key = \"[redacted aws-secret]\"`},
		{`url = https://x-access-token:ghp_abcdefghijklmnopqrstuvwxyz0123456789@github.example/o/r.git`,
			`url = https://x-access-token:[redacted github-token]@github.example/o/r.git`},
		{`OPENAI_API_KEY = \"sk-proj-abcdefghijklmnopqrstuvwxyzABCDEFGHIJ_-0123456789\"`, `OPENAI_API_KEY = \"[redacted api-key]\"`},
		{`curl -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc'`, `curl -H 'Authorization: Bearer [redacted bearer-token]'`},
		{`AUTHORIZATION: BEARER 0123456789abcdef0123`, `AUTHORIZATION: BEARER [redacted bearer-token]`},
		{`-----BEGIN RSA PRIVATE KEY-----\nMIIEow\nIBAAK\n-----END RSA PRIVATE KEY-----\nrest`, `[redacted private-key]\nrest`},
		{`token xoxb-1234567890-abcdefghij here`, `token [redacted slack-token] here`},
		// Not secrets: a word that ends in sk-, a short key, a repository on github.
		{`task-abcdefghijklmnopqrstuvwxyz0123456789 and sk-short and github.com/ghp_x`, `task-abcdefghijklmnopqrstuvwxyz0123456789 and sk-short and github.com/ghp_x`},
	}
	for _, c := range cases {
		got, n := Redact(c.in)
		if got != c.out {
			t.Errorf("Redact(%q)\n got %q\nwant %q", c.in, got, c.out)
		}
		if want := strings.Count(c.out, "[redacted"); n != want {
			t.Errorf("Redact(%q) masked %d, want %d", c.in, n, want)
		}
	}
}

// TestRedactPrefilter checks that the quick tests before each pattern never skip a match: text made
// of fragments near the patterns' shapes is masked the same with them and without them.
func TestRedactPrefilter(t *testing.T) {
	frags := []string{"AKIA", "ABCDEFGHIJKLMNOP", "aWs_SeCrEt_AcCeSs_KeY", "AWS_SECRET_ACCESS_KEY", "=", " : ", `\"`, "'",
		fakeAWSSecret, "gh", "ghp_", "gho_", "ghx_", "abcdefghijklmnopqrstuvwxyz0123456789", "sk-", "proj-",
		"xoxb-", "xoxq-", "1234567890", "Authorization", "AUTHORIZATION", "authorization: ", "Bearer ", "bEaReR  ", "-----BEGIN RSA PRIVATE KEY-----",
		"-----END RSA PRIVATE KEY-----", "PRIVATE KEY", " ", "/", "_", "é", "\\n", "x"}
	rnd := rand.New(rand.NewSource(7))
	plain := func(s string) (string, int) {
		n := 0
		for _, p := range secrets {
			n += len(p.re.FindAllStringIndex(s, -1))
			s = p.re.ReplaceAllString(s, p.with)
		}
		return s, n
	}
	masked := 0
	for k := 0; k < 20000; k++ {
		var b strings.Builder
		for j := rnd.Intn(12); j >= 0; j-- {
			b.WriteString(frags[rnd.Intn(len(frags))])
		}
		s := b.String()
		got, n := Redact(s)
		want, wn := plain(s)
		if got != want || n != wn {
			t.Fatalf("%q: masked to %q (%d), without the quick tests %q (%d)", s, got, n, want, wn)
		}
		masked += n
	}
	if masked == 0 {
		t.Fatal("the fragments made no secret at all")
	}
}

// A system prompt and a tool list that every run sends with every call.
const systemPrompt = "You are a coding agent. Work in /workspace. Treat credentials you come across as secret."
const toolList = `[{"type":"function","function":{"name":"execute_bash","description":"Run a command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}]`

type testMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

func message(role, source, content string, at int64) Message {
	name := ""
	if strings.HasPrefix(source, "tool:") {
		name = source[5:]
	}
	raw, _ := json.Marshal(testMsg{role, content, name})
	return Message{Raw: string(raw), Role: role, Source: source, Tokens: int64(len(content)/4 + 4), At: at}
}

// testRuns are three small runs: two on one repository that overlap in time, one of them with keys
// in the environment, and one on another repository with a bearer token and a long wait that lets
// the provider's cache expire.
func testRuns() []*Run {
	start := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC).Unix()
	a := &Run{ID: "run-a", Repo: "acme/shop", Model: "test-coder", Start: start, Tools: toolList, ToolsTokens: 40, Messages: []Message{
		message("system", "system", systemPrompt, 0),
		message("user", "task", "The cart total is wrong when a coupon is applied twice.", 0),
		message("assistant", "assistant", "Let me look at the environment first.", 3),
		message("tool", "tool:execute_bash", "HOME=/root\nAWS_ACCESS_KEY_ID="+fakeAWSKeyID+"\nAWS_SECRET_ACCESS_KEY="+fakeAWSSecret+"\nTERM=xterm", 4),
		message("assistant", "assistant", "Now the cart module.", 7),
		message("tool", "tool:execute_bash", strings.Repeat("def total(cart):\n    return sum(i.price for i in cart)\n", 40), 8),
		message("assistant", "assistant", "Fixed: the coupon is applied once.", 12),
	}}
	b := &Run{ID: "run-b", Repo: "acme/shop", Model: "test-coder", Start: start + 5, Tools: toolList, ToolsTokens: 40, Messages: []Message{
		message("system", "system", systemPrompt, 0),
		message("user", "task", "Search returns nothing for words with accents.", 0),
		message("assistant", "assistant", "Let me run the search tests.", 2),
		message("tool", "tool:execute_bash", strings.Repeat("test_search.py::test_accents FAILED\n", 30), 9),
		message("assistant", "assistant", "The index folds case but not accents; fixed.", 11),
	}}
	c := &Run{ID: "run-c", Repo: "acme/blog", Model: "test-coder", Start: start + 2, Tools: toolList, ToolsTokens: 40, Messages: []Message{
		message("system", "system", systemPrompt, 0),
		message("user", "task", "The feed endpoint rejects valid tokens.", 0),
		message("assistant", "assistant", "Let me call the endpoint.", 4),
		message("tool", "tool:execute_bash", "> GET /feed\n> Authorization: Bearer 0123456789abcdefghijKLMNOP\n< 401", 400),
		message("assistant", "assistant", "The header is read case-sensitively; fixed.", 405),
	}}
	return []*Run{a, b, c}
}

func readPolicy(t testing.TB) *policy.Policy {
	src, err := os.ReadFile(filepath.Join("..", "examples", "traces.precompute"))
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

func openStore(t testing.TB, path string) (*sqlitestore.Store, *engine.Engine, *Store) {
	t.Helper()
	st, err := sqlitestore.Open(path, sqlitestore.Options{Sync: "off"})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Open(st, readPolicy(t), "traces.precompute")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(eng)
	if err != nil {
		t.Fatal(err)
	}
	return st, eng, s
}

func dayOf(runs []*Run) *Day {
	d := &Day{byID: map[string]*Run{}}
	for _, r := range runs {
		d.add(r)
	}
	d.order()
	return d
}

func one(t testing.TB, st engine.Store, sql string, args ...any) any {
	t.Helper()
	var v any
	if err := st.Query(sql, args, func(row []any) error { v = row[0]; return nil }); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func num(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

// masked is a run's messages and tool list, each masked on its own.
func masked(r *Run) ([]string, string) {
	var msgs []string
	for _, m := range r.Messages {
		body, _ := Redact(m.Raw)
		msgs = append(msgs, body)
	}
	tools, _ := Redact(r.Tools)
	return msgs, tools
}

// checkCalls rebuilds every call of the runs from the file and compares it with the request as
// sent, to the byte, and with the size and SHA-256 the store wrote down when it kept the call.
func checkCalls(t *testing.T, st engine.Store, runs []*Run) int {
	t.Helper()
	n := 0
	for _, r := range runs {
		msgs, tools := masked(r)
		for seq, i := range r.Replies() {
			id := r.CallID(seq + 1)
			req, reply, err := Rebuild(st, id)
			if err != nil {
				t.Fatal(err)
			}
			want := Request(r.Model, msgs[:i], tools) // the request the long way
			if req != want {
				t.Fatalf("%s rebuilt differs from the request as sent:\n got %.300s\nwant %.300s", id, req, want)
			}
			if reply != msgs[i] {
				t.Fatalf("%s: reply differs", id)
			}
			sum := sha256.Sum256([]byte(req))
			if got := one(t, st, "SELECT request_sha256 FROM trace_calls WHERE call_id = ?", id); got != hex.EncodeToString(sum[:]) {
				t.Fatalf("%s: SHA-256 %v, the rebuilt request has %x", id, got, sum)
			}
			if got := num(one(t, st, "SELECT request_bytes FROM trace_calls WHERE call_id = ?", id)); got != int64(len(req)) {
				t.Fatalf("%s: %d bytes written down, %d rebuilt", id, got, len(req))
			}
			n++
		}
	}
	return n
}

// cost is a call's cost at the policy's example prices, in billionths of a dollar.
func cost(u Usage) int64 { return (u.Input-u.Cached)*400 + u.Cached*40 + u.Output*1600 }

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traces.db")
	st, eng, s := openStore(t, path)
	runs := testRuns()
	day := dayOf(runs)
	if len(day.Calls) != 7 {
		t.Fatalf("%d calls, want 7", len(day.Calls))
	}
	rp := &Replay{Day: day, Store: s}
	if _, err := rp.Until(runs[0].Start + 8); err != nil {
		t.Fatal(err)
	}
	if err := eng.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if _, err := rp.Until(1 << 40); err != nil || !rp.Done() {
		t.Fatalf("replay: %v, done %v", err, rp.Done())
	}
	if err := eng.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if n := checkCalls(t, st, runs); n != 7 {
		t.Fatalf("%d calls rebuilt", n)
	}
	stats := s.Stats()
	t.Logf("%+v", stats)
	if stats.Masked != 3 {
		t.Errorf("%d secrets masked, want 3", stats.Masked)
	}
	for _, secret := range []string{fakeAWSKeyID, fakeAWSSecret, "0123456789abcdefghijKLMNOP"} {
		if n := num(one(t, st, "SELECT count(*) FROM trace_pieces WHERE instr(body, ?) > 0", secret)); n != 0 {
			t.Errorf("a secret is in the file: %s", secret)
		}
	}
	// The system prompt and the tool list are kept once for the three runs.
	for _, src := range []string{"system", "tools"} {
		if n := num(one(t, st, "SELECT count(*) FROM trace_pieces WHERE source = ?", src)); n != 1 {
			t.Errorf("%d pieces from %s, want 1", n, src)
		}
	}
	if stats.PieceBytes >= stats.RequestBytes {
		t.Errorf("pieces take %d bytes, the requests %d", stats.PieceBytes, stats.RequestBytes)
	}
	// The cache: run-c's second call waits 396 s after its first, longer than the cache keeps it.
	if u := runs[2].UsageOf(4); u.Cached != 0 {
		t.Errorf("run-c call 2 has %d cached tokens; the cache had expired", u.Cached)
	}
	if u := runs[0].UsageOf(4); u.Cached == 0 || u.Cached >= u.Input {
		t.Errorf("run-a call 2: %d of %d tokens cached", u.Cached, u.Input)
	}
	// The meter: every run's cost, and the same totals by call and by source.
	var total int64
	for _, r := range runs {
		var want int64
		for _, i := range r.Replies() {
			want += cost(r.UsageOf(i))
		}
		total += want
		if got := num(one(t, st, "SELECT value FROM run_cost WHERE run = ?", r.ID)); got != want {
			t.Errorf("%s costs %d in the file, %d counted", r.ID, got, want)
		}
		if got := num(one(t, st, "SELECT value FROM run_calls WHERE run = ?", r.ID)); got != int64(len(r.Replies())) {
			t.Errorf("%s: %d calls in the file", r.ID, got)
		}
	}
	bySource := num(one(t, st, "SELECT sum(value) FROM source_cost_day"))
	byRepo := num(one(t, st, "SELECT sum(value) FROM repo_cost_day"))
	if bySource != total || byRepo != total {
		t.Errorf("total %d; by source %d, by repository %d", total, bySource, byRepo)
	}
	// A budget.
	if err := st.Exec("INSERT INTO repo_budget_limit (repo, lim) VALUES ('acme/shop', 1000), ('acme/blog', 1e12)"); err != nil {
		t.Fatal(err)
	}
	if got := num(one(t, st, "SELECT count(*) FROM repo_budget WHERE reached")); got != 1 {
		t.Errorf("%d budgets reached, want 1", got)
	}

	// The same run reported again: the store knows its calls, the streams refuse the events.
	if n, err := s.Again(runs[0]); err != nil || n != 3 {
		t.Fatalf("again: %d, %v", n, err)
	}
	if err := eng.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if got := num(one(t, st, "SELECT sum(value) FROM repo_cost_day")); got != total {
		t.Errorf("after the repeat the total is %d, was %d", got, total)
	}
	if got := num(one(t, st, "SELECT n FROM calls_refused WHERE reason = 'repeat'")); got != 3 {
		t.Errorf("%d calls refused as repeats, want 3", got)
	}
	if s.Stats().Repeats != 3 {
		t.Errorf("%d repeats counted", s.Stats().Repeats)
	}
	pieces := num(one(t, st, "SELECT count(*) FROM trace_pieces"))
	st.Close()

	// Open the file again: a new run reuses the pieces already kept, and a known call is a repeat.
	st, eng, s = openStore(t, path)
	defer st.Close()
	d := &Run{ID: "run-d", Repo: "acme/blog", Model: "test-coder", Start: runs[0].Start + 600, Tools: toolList, ToolsTokens: 40, Messages: []Message{
		message("system", "system", systemPrompt, 0),
		message("user", "task", "Add a sitemap.", 0),
		message("assistant", "assistant", "Done.", 3),
	}}
	if err := s.Put(d, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(runs[1], 1); err != nil {
		t.Fatal(err)
	}
	if err := eng.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if got := num(one(t, st, "SELECT count(*) FROM trace_pieces")); got != pieces+2 {
		t.Errorf("%d pieces after run-d, want %d (its task and its reply)", got, pieces+2)
	}
	if s.Stats().Repeats != 1 || s.Stats().Calls != 1 {
		t.Errorf("after reopening: %+v", s.Stats())
	}
	checkCalls(t, st, append(runs, d))
}

// TestGeneratedDay replays the day of Demo 8 when its data is there, and rebuilds every call.
func TestGeneratedDay(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "demo", "traces", "app", "data", "day.jsonl.gz"))
	if err != nil {
		t.Skip("no prepared day: node tools/traces-prepare.mjs")
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	day, err := ReadDay(gz)
	if err != nil {
		t.Fatal(err)
	}
	st, eng, s := openStore(t, ":memory:")
	defer st.Close()
	rp := &Replay{Day: day, Store: s}
	t0 := time.Now()
	for !rp.Done() {
		if _, err := rp.Until(day.Calls[rp.Next].TS + 60); err != nil {
			t.Fatal(err)
		}
		if err := eng.Checkpoint(); err != nil {
			t.Fatal(err)
		}
	}
	took := time.Since(t0)
	stats := s.Stats()
	n := checkCalls(t, st, day.Runs)
	var total int64
	for _, c := range day.Calls {
		total += cost(c.Run.UsageOf(c.Run.Replies()[c.Seq-1]))
	}
	if got := num(one(t, st, "SELECT sum(value) FROM repo_cost_day")); got != total {
		t.Errorf("cost %d in the file, %d counted", got, total)
	}
	if got := num(one(t, st, "SELECT sum(value) FROM source_cost_day")); got != total {
		t.Errorf("cost by source %d, counted %d", got, total)
	}
	var left []string
	st.Query("SELECT body FROM trace_pieces", nil, func(row []any) error {
		for _, p := range Patterns {
			if m := regexp.MustCompile(p.Re).FindString(row[0].(string)); m != "" && !strings.Contains(m, "[redacted") {
				left = append(left, m)
			}
		}
		return nil
	})
	if len(left) > 0 {
		t.Errorf("secrets left in the file: %q", left)
	}
	if stats.Masked == 0 {
		t.Errorf("the day has planted secrets and none was masked")
	}
	pieceTable := num(one(t, st, "SELECT sum(pgsize) FROM dbstat WHERE name IN ('trace_pieces', 'trace_calls', 'sqlite_autoindex_trace_pieces_1', 'trace_calls_run')"))
	t.Logf("%d runs, %d calls in %v, all %d rebuilt to the byte; %d secrets masked", len(day.Runs), len(day.Calls), took.Round(time.Millisecond), n, stats.Masked)
	t.Logf("requests as sent %s, replies %s; pieces %s (%d); trace tables %s; total cost $%.2f",
		mb(stats.RequestBytes), mb(stats.ReplyBytes), mb(stats.PieceBytes), stats.Pieces, mb(pieceTable), float64(total)/1e9)
}

func mb(n int64) string { return fmt.Sprintf("%.2f MB", float64(n)/1e6) }
