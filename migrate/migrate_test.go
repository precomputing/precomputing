//go:build cgo

package migrate

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/engine/sqlitestore"
	"precomputing.com/precomputing/internal/sqlite"
	"precomputing.com/precomputing/policy"
)

func readExample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type ev struct {
	ts       int64
	endpoint string
	ms       float64
}

// latency: five endpoints, lognormal latencies with slow spikes and late events.
func latency(n int, from int64, seed int64) []ev {
	rnd := rand.New(rand.NewSource(seed))
	eps := []string{"/api/search", "/api/cart", "/api/checkout", "/api/login", "/api/items"}
	ts := from
	var out []ev
	for i := 0; i < n; i++ {
		if rnd.Intn(4) == 0 {
			ts++
		}
		ms := 40 * math.Exp(rnd.NormFloat64()*0.5)
		if rnd.Intn(400) == 0 {
			ms *= 25
		}
		at := ts
		if rnd.Intn(200) == 0 {
			at -= int64(rnd.Intn(900))
		}
		out = append(out, ev{at, eps[rnd.Intn(len(eps))], math.Round(ms*1000) / 1000})
	}
	return out
}

// v2 adds to Demo 1's policy: a rollup, a precompute with its own state, a stream, a quota, and
// longer raw events.
func v2(v1 string) string {
	s := strings.Replace(v1, "raw     keep 5m", "raw     keep 15m", 1)
	s = strings.Replace(s, "  rollup  1h  keep 1y   quantiles ms", "  rollup  1h  keep 1y   quantiles ms\n  rollup  1d  keep forever quantiles ms", 1)
	return s + `
precompute slowest_ms = max(latency.ms) by endpoint per hour
precompute p50_hour   = p50(latency.ms) per hour

stream errors {
  key   endpoint text
  value code integer
  rollup 1m keep 30d
}

precompute errors_day = count(errors) by endpoint per day
quota error_budget = errors_day
`
}

func sqlFile(t *testing.T, path, src string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	out, err := compile.Source(src, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(out.Schema); err != nil {
		t.Fatal(err)
	}
	return db
}

// feedSQL inserts events through the triggers and then runs distill with :now at the newest.
func feedSQL(t *testing.T, db *sqlite.DB, evs []ev, now int64) {
	t.Helper()
	db.Exec("BEGIN")
	ins, err := db.PrepareOnce("INSERT INTO latency (ts, endpoint, ms) VALUES (?, ?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		ins.Bind(1, e.ts)
		ins.Bind(2, e.endpoint)
		ins.Bind(3, e.ms)
		if _, err := ins.Step(); err != nil {
			t.Fatal(err)
		}
		ins.Reset()
	}
	ins.Finalize()
	distill(t, db, now)
	db.Exec("COMMIT")
}

func distill(t *testing.T, db *sqlite.DB, now int64) {
	t.Helper()
	text, err := one(db, "SELECT value FROM _precomputing WHERE key = 'distill'")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range strings.Split(text, ";\n") {
		var lines []string
		for _, l := range strings.Split(q, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "--") {
				lines = append(lines, l)
			}
		}
		if len(lines) == 0 {
			continue
		}
		st, err := db.PrepareOnce(strings.TrimSuffix(strings.Join(lines, "\n"), ";"))
		if err != nil {
			t.Fatal(err)
		}
		st.Bind(st.ParamIndex(":now"), now)
		if _, err := st.Step(); err != nil {
			t.Fatal(err)
		}
		st.Finalize()
	}
}

// feedEngine puts events through an Engine on the file and checkpoints once.
func feedEngine(t *testing.T, path, src string, evs []ev) {
	t.Helper()
	st, err := sqlitestore.Open(path, sqlitestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var pol *policy.Policy
	if src != "" {
		if pol, err = policy.Parse(src); err != nil {
			t.Fatal(err)
		}
	}
	eng, err := engine.Open(st, pol, "test")
	if err != nil {
		t.Fatal(err)
	}
	s := eng.Stream("latency")
	for _, e := range evs {
		k, err := eng.Key(s, e.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.Put(s, "", 0, e.ts, k, []float64{e.ms}); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.Checkpoint(); err != nil {
		t.Fatal(err)
	}
}

func migrateFile(t *testing.T, path, src string) *Plan {
	t.Helper()
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := Prepare(db, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(db, 1790600000); err != nil {
		t.Fatal(err)
	}
	return p
}

// dump is every row of every table but the Engine's own, in a fixed order.
func dump(t *testing.T, db *sqlite.DB, skip ...string) map[string]string {
	t.Helper()
	tables, err := rows(db, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
outer:
	for _, r := range tables {
		name := r[0].(string)
		for _, s := range skip {
			if name == s {
				continue outer
			}
		}
		rs, err := rows(db, "SELECT * FROM "+quote(name)+" ORDER BY 1, 2, 3")
		if err != nil {
			rs, err = rows(db, "SELECT * FROM "+quote(name)+" ORDER BY 1")
		}
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, row := range rs {
			fmt.Fprintln(&b, row...)
		}
		out[name] = b.String()
	}
	return out
}

func sameTables(t *testing.T, what string, a, b map[string]string, only ...string) {
	t.Helper()
	names := only
	if names == nil {
		for k := range a {
			names = append(names, k)
		}
		for k := range b {
			if _, ok := a[k]; !ok {
				t.Errorf("%s: table %s only in the second", what, k)
			}
		}
	}
	for _, k := range names {
		if a[k] != b[k] {
			t.Errorf("%s: table %s differs (%d and %d bytes)", what, k, len(a[k]), len(b[k]))
		}
	}
}

// After a migration both runtimes go on writing the same file, and what the old policy counted
// goes on exactly as in a file that was never migrated.
func TestMigrateBothRuntimes(t *testing.T) {
	v1 := readExample(t, "latency.precompute")
	src2 := v2(v1)
	e1 := latency(30000, 1790586000, 1)
	e2 := latency(30000, 1790586000+9000, 2)
	last := func(evs []ev) int64 {
		m := int64(0)
		for _, e := range evs {
			m = max(m, e.ts)
		}
		return m
	}
	dir := t.TempDir()

	sqlPath := filepath.Join(dir, "sql.db")
	db := sqlFile(t, sqlPath, v1)
	feedSQL(t, db, e1, last(e1))
	db.Close()
	plan := migrateFile(t, sqlPath, src2)
	db, _ = sqlite.Open(sqlPath)
	feedSQL(t, db, e2, max(last(e1), last(e2)))

	engPath := filepath.Join(dir, "eng.db")
	feedEngine(t, engPath, v1, e1)
	migrateFile(t, engPath, src2)
	feedEngine(t, engPath, "", e2)
	edb, _ := sqlite.Open(engPath)
	defer edb.Close()

	got := dump(t, edb, engine.SourcesTable)
	sameTables(t, "triggers and Engine after a migration", dump(t, db), got)

	// The old policy's tables, in a file that was never migrated.
	plainPath := filepath.Join(dir, "plain.db")
	plain := sqlFile(t, plainPath, v1)
	feedSQL(t, plain, e1, last(e1))
	feedSQL(t, plain, e2, max(last(e1), last(e2)))
	want := dump(t, plain)
	// latency_raw differs by design: v2 keeps raw events for 15 minutes instead of 5. The windows
	// and sketches of the new 1d rollup share their tables with the old rollups'; leave them out.
	for _, tbl := range []string{"latency_win", "latency_ms_sk"} {
		rs, _ := rows(edb, "SELECT * FROM "+tbl+" WHERE res <> 86400 ORDER BY 1, 2, 3")
		var b strings.Builder
		for _, row := range rs {
			fmt.Fprintln(&b, row...)
		}
		got[tbl] = b.String()
	}
	sameTables(t, "old tables against a file never migrated", want, got,
		"latency_win", "latency_ms_sk", "latency_sample", "latency_anomaly", "latency_base", "_pc_latency_by_endpoint", "_pcq_latency_ms_by_endpoint")
	if !strings.Contains(strings.Join(plan.Added, "\n"), "rollup 1d") || !strings.Contains(strings.Join(plan.Changed, "\n"), "raw events for 15m") {
		t.Errorf("plan: added %q, changed %q", plan.Added, plan.Changed)
	}
	// The 1d rollup holds only what came after the migration; the 1h one holds everything.
	n1d, _ := one(edb, "SELECT CAST(sum(n) AS TEXT) FROM latency_win WHERE res = 86400")
	n1h, _ := one(edb, "SELECT CAST(sum(n) AS TEXT) FROM latency_win WHERE res = 3600")
	if n1d != fmt.Sprint(len(e2)) || n1h != fmt.Sprint(len(e1)+len(e2)) {
		t.Errorf("events in the 1d windows %s (want %d), in the 1h windows %s (want %d)", n1d, len(e2), n1h, len(e1)+len(e2))
	}
	hist, err := History(edb)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history: %v %v", hist, err)
	}
	// Migrating again to the same policy changes nothing.
	p, err := Prepare(edb, src2)
	if err != nil || len(p.Added)+len(p.Changed) != 0 {
		t.Errorf("a second migration: %+v %v", p, err)
	}
}

func TestMigrateRefuses(t *testing.T) {
	v1 := readExample(t, "latency.precompute")
	usage := readExample(t, "usage.precompute")
	renamed := strings.ReplaceAll(v1, "latency", "lat")
	for _, c := range []struct{ base, name, from, to, want string }{
		{v1, "a stream renamed", v1, renamed, "stream latency would go"},
		{v1, "a key changed", "key   endpoint text", "key   endpoint text\n  key   region text", "keys would change"},
		{v1, "a value's type changed", "value ms real", "value ms integer", "values would change"},
		{v1, "a rollup removed", "  rollup  10s keep 24h", "", "rollup 10s would go"},
		{v1, "quantiles added to a rollup", "rollup  10s keep 24h", "rollup  10s keep 24h quantiles ms", "keeps the quantiles it had"},
		{v1, "the accuracy changed", "  samples 3 per 1m", "  samples 3 per 1m\n  quantile accuracy 2%", "quantile accuracy stays 1%"},
		{v1, "raw events dropped", "  raw     keep 5m", "", "stop keeping raw events"},
		{v1, "anomalies of another kind", "anomalies ms log z > 4", "anomalies ms z > 4", "anomalies would judge something else"},
		{v1, "a precompute changed", "precompute avg_ms   = avg(latency.ms) by endpoint", "precompute avg_ms   = avg(latency.ms)", "precompute avg_ms would change"},
		{v1, "a column added to shared state", "precompute p99_ms", "precompute top_ms = max(latency.ms) by endpoint\nprecompute p99_ms", "would add a column"},
		{usage, "the period changed", "period month close 24h", "period day close 24h", "period stays month"},
		{usage, "the close changed", "period month close 24h", "period month close 48h", "close stays 1d"},
		{usage, "late changed", "late   48h", "late   72h", "late stays"},
		{usage, "a quota removed", "quota monthly_tokens = tokens_month", "", "quota monthly_tokens would go"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(c.base, c.from) {
				t.Fatalf("%q is not in the policy", c.from)
			}
			path := filepath.Join(t.TempDir(), "f.db")
			db := sqlFile(t, path, c.base)
			before := dump(t, db)
			_, err := Prepare(db, strings.Replace(c.base, c.from, c.to, 1))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			sameTables(t, "a refused migration", before, dump(t, db))
		})
	}
}

// What an exact stream may change: ahead, how long identifiers and raw events are kept.
func TestMigrateExact(t *testing.T) {
	usage := readExample(t, "usage.precompute")
	path := filepath.Join(t.TempDir(), "f.db")
	db := sqlFile(t, path, usage)
	src := strings.Replace(usage, "late   48h", "late   48h\n  ahead  1h", 1)
	src = strings.Replace(src, "raw    until closed + 90d", "raw    until closed + 120d", 1)
	p, err := Prepare(db, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(db, 1790600000); err != nil {
		t.Fatal(err)
	}
	trig, _ := one(db, "SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'usage_ingest'")
	if !strings.Contains(trig, "'ahead'") {
		t.Errorf("the trigger has no ahead check after the migration")
	}
	if pol, _ := one(db, "SELECT value FROM _precomputing WHERE key = 'policy'"); pol != src {
		t.Errorf("the file's policy is not the new one")
	}
	// A report years ahead is now refused as ahead.
	if err := db.Exec("INSERT INTO usage (ts, request_id, customer, model, gateway, input_tokens, output_tokens) VALUES (4102444800, 'x', 'a', 'm', 'g', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	if got, _ := one(db, "SELECT reason || ' ' || n FROM usage_refused"); got != "ahead 1" {
		t.Errorf("refused: %q", got)
	}
}

// A migration that fails part way leaves the file exactly as it was.
func TestMigrateFailsWhole(t *testing.T) {
	v1 := readExample(t, "latency.precompute")
	path := filepath.Join(t.TempDir(), "f.db")
	db := sqlFile(t, path, v1)
	feedSQL(t, db, latency(5000, 1790586000, 3), 1790586000+1250)
	before := dump(t, db)
	schema, _ := rows(db, "SELECT type, name, sql FROM sqlite_master ORDER BY name")
	p, err := Prepare(db, v2(v1))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 3, len(p.drop) + 2, len(p.drop) + len(p.create)} {
		failAfter = n
		err := p.Apply(db, 1790600000)
		failAfter = -1
		if err == nil {
			t.Fatalf("failing after %d statements: no error", n)
		}
		sameTables(t, fmt.Sprintf("failed after %d statements", n), before, dump(t, db))
		after, _ := rows(db, "SELECT type, name, sql FROM sqlite_master ORDER BY name")
		if fmt.Sprint(after) != fmt.Sprint(schema) {
			t.Errorf("failed after %d statements: the schema changed", n)
		}
	}
	// And then it goes through.
	if err := p.Apply(db, 1790600000); err != nil {
		t.Fatal(err)
	}
}

// A name the new policy needs that the file already uses for something of its own is refused,
// rather than taken over.
func TestMigrateNameInUse(t *testing.T) {
	v1 := readExample(t, "latency.precompute")
	path := filepath.Join(t.TempDir(), "f.db")
	db := sqlFile(t, path, v1)
	if err := db.Exec("CREATE TABLE errors_win (note TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(db, v2(v1)); err == nil || !strings.Contains(err.Error(), "already has a table named errors_win") {
		t.Fatalf("got %v", err)
	}
}

// A new precompute that shares state with an old one answers from the start of the file, and the
// plan says so instead of calling it new.
func TestMigrateSharedState(t *testing.T) {
	v1 := readExample(t, "latency.precompute")
	path := filepath.Join(t.TempDir(), "f.db")
	db := sqlFile(t, path, v1)
	feedSQL(t, db, latency(2000, 1790586000, 4), 1790586000+500)
	p, err := Prepare(db, v1+"\nprecompute n_ep = count(latency) by endpoint\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Added) != 0 || len(p.Changed) != 1 || !strings.Contains(p.Changed[0], "answers from the start of the file") {
		t.Fatalf("plan: added %q, changed %q", p.Added, p.Changed)
	}
	if err := p.Apply(db, 1790600000); err != nil {
		t.Fatal(err)
	}
	if got, _ := one(db, "SELECT CAST(sum(value) AS TEXT) FROM n_ep"); got != "2000" {
		t.Errorf("n_ep holds %s events, want all 2000", got)
	}
}

// A trigger on a compiled table that the policy doesn't make is refused: dropped silently it would be
// lost, and left in place beside the new trigger it could count events twice.
func TestMigrateForeignTrigger(t *testing.T) {
	v1 := readExample(t, "latency.precompute")
	path := filepath.Join(t.TempDir(), "f.db")
	db := sqlFile(t, path, v1)
	if err := db.Exec("CREATE TABLE audit (ts INTEGER); CREATE TRIGGER my_audit AFTER INSERT ON latency_raw BEGIN INSERT INTO audit VALUES (NEW.ts); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(db, v2(v1)); err == nil || !strings.Contains(err.Error(), "trigger my_audit on latency_raw") {
		t.Fatalf("got %v", err)
	}
	// A trigger on the owner's own table is theirs and stays out of it.
	db.Exec("DROP TRIGGER my_audit; CREATE TABLE mine (x INTEGER); CREATE TRIGGER mine_t AFTER INSERT ON mine BEGIN SELECT 1; END")
	if _, err := Prepare(db, v2(v1)); err != nil {
		t.Fatal(err)
	}
}
