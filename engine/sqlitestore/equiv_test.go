//go:build cgo

package sqlitestore_test

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

type event struct {
	ts   int64
	keys []any
	vals []float64
	id   any
}

func readPolicy(t *testing.T, name string) *policy.Policy {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "..", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(string(text))
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

// latencyEvents: five endpoints, lognormal latencies, slow spikes, a few zeros, and late events.
func latencyEvents(n int, seed int64) []event {
	rnd := rand.New(rand.NewSource(seed))
	eps := []string{"/api/search", "/api/cart", "/api/checkout", "/api/login", "/api/items"}
	med := []float64{30, 45, 120, 80, 25}
	ts := int64(1790586000)
	var out []event
	for i := 0; i < n; i++ {
		if rnd.Intn(8) == 0 {
			ts++
		}
		e := rnd.Intn(len(eps))
		ms := med[e] * math.Exp(rnd.NormFloat64()*0.4)
		switch {
		case rnd.Intn(500) == 0:
			ms *= 20 // a slow request
		case rnd.Intn(2000) == 0:
			ms = 0 // not judged, zero bucket
		}
		ms = math.Round(ms*1000) / 1000
		at := ts
		if rnd.Intn(300) == 0 {
			at -= int64(rnd.Intn(1200)) // up to 20 minutes late
		}
		out = append(out, event{ts: at, keys: []any{eps[e]}, vals: []float64{ms}})
	}
	return out
}

// tradesEvents: three symbols across midnight UTC, a price jump, integer sizes, late trades.
func tradesEvents(n int, seed int64) []event {
	rnd := rand.New(rand.NewSource(seed))
	syms := []string{"AAA", "BBB", "CCC"}
	price := []float64{101.5, 20.25, 7.4}
	ts := int64(1790640000 - 1800) // half an hour before midnight
	var out []event
	for i := 0; i < n; i++ {
		if rnd.Intn(6) == 0 {
			ts++
		}
		s := rnd.Intn(len(syms))
		price[s] *= math.Exp(rnd.NormFloat64() * 0.0004)
		if i == n/2 {
			price[s] *= 1.07
		}
		p := math.Round(price[s]*100) / 100
		size := float64(100 * (1 + rnd.Intn(20)))
		if rnd.Intn(5) == 0 {
			size = float64(1 + rnd.Intn(99))
		}
		at := ts
		if rnd.Intn(400) == 0 {
			at -= int64(rnd.Intn(900))
		}
		out = append(out, event{ts: at, keys: []any{syms[s]}, vals: []float64{p, size}})
	}
	return out
}

// sqlRuntime creates the compiled schema and feeds events to its triggers, distilling every so many events.
func sqlRuntime(t *testing.T, db *sqlite.DB, pol *policy.Policy, name, stream string, evs []event, distillEvery int) int64 {
	t.Helper()
	lay, err := compile.NewLayout(pol, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(lay.Output.Schema); err != nil {
		t.Fatal(err)
	}
	return sqlFeed(t, db, lay, stream, evs, distillEvery, 0)
}

// sqlContinue feeds events to the triggers of a file that already holds a policy.
func sqlContinue(t *testing.T, st *sqlitestore.Store, stream string, evs []event, distillEvery int) int64 {
	t.Helper()
	var text string
	st.Query("SELECT value FROM _precomputing WHERE key = 'policy'", nil, func(r []any) error { text = r[0].(string); return nil })
	pol, err := policy.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	lay, err := compile.NewLayout(pol, "trades.precompute")
	if err != nil {
		t.Fatal(err)
	}
	var now int64
	st.Query("SELECT max(newest) FROM "+engine.SourcesTable, nil, func(r []any) error { now, _ = r[0].(int64); return nil })
	return sqlFeed(t, st.DB(), lay, stream, evs, distillEvery, now)
}

func sqlFeed(t *testing.T, db *sqlite.DB, lay *compile.Layout, stream string, evs []event, distillEvery int, now int64) int64 {
	t.Helper()
	s := lay.Policy.Stream(stream)
	cols := []string{"ts"}
	if s.ID != nil {
		cols = append(cols, s.ID.Name)
	}
	for _, k := range s.Keys {
		cols = append(cols, k.Name)
	}
	for _, v := range s.Values {
		cols = append(cols, v.Name)
	}
	ins, err := db.Prepare(fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", stream, strings.Join(cols, ", "), strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")))
	if err != nil {
		t.Fatal(err)
	}
	distill := func() {
		for _, q := range lay.Distill {
			st, err := db.Prepare(q)
			if err != nil {
				t.Fatal(err)
			}
			st.Bind(st.ParamIndex(":now"), now)
			if _, err := st.Step(); err != nil {
				t.Fatal(err)
			}
			st.Reset()
		}
	}
	db.Exec("BEGIN")
	for i, e := range evs {
		ins.Bind(1, e.ts)
		first := 2
		if s.ID != nil {
			ins.Bind(2, e.id)
			first = 3
		}
		for j, k := range e.keys {
			ins.Bind(first+j, k)
		}
		for j, v := range e.vals {
			if s.Values[j].Type == "integer" {
				ins.Bind(first+len(e.keys)+j, int64(v))
			} else {
				ins.Bind(first+len(e.keys)+j, v)
			}
		}
		if _, err := ins.Step(); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		ins.Reset()
		if e.ts > now {
			now = e.ts
		}
		if (i+1)%distillEvery == 0 {
			distill()
		}
	}
	distill()
	db.Exec("COMMIT")
	return now
}

// runEngine feeds events to an Engine with sequence numbers, checkpointing at uneven intervals.
func runEngine(t *testing.T, eng *engine.Engine, stream string, evs []event, from int, seed int64) {
	t.Helper()
	rnd := rand.New(rand.NewSource(seed))
	si := eng.Stream(stream)
	next := from + 500 + rnd.Intn(4000)
	for i := from; i < len(evs); i++ {
		e := evs[i]
		key, err := eng.Key(si, e.keys...)
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.PutID(si, "feed", int64(i+1), e.ts, e.id, key, e.vals); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if i == next {
			if err := eng.Checkpoint(); err != nil {
				t.Fatal(err)
			}
			next = i + 500 + rnd.Intn(4000)
		}
	}
	if err := eng.Checkpoint(); err != nil {
		t.Fatal(err)
	}
}

func tables(t *testing.T, db *sqlite.DB) map[string]bool {
	out := map[string]bool{}
	st, err := db.Prepare("SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> '" + engine.SourcesTable + "'")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Reset()
	for {
		ok, err := st.Step()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		out[st.Text(0)] = strings.Contains(st.Text(1), "WITHOUT ROWID")
	}
}

func dump(t *testing.T, db *sqlite.DB, table string, withoutRowid bool) [][]any {
	st, err := db.PrepareOnce("SELECT * FROM " + table)
	if err != nil {
		t.Fatal(err)
	}
	ncol := st.Columns()
	st.Finalize()
	order := "rowid"
	if withoutRowid {
		var cols []string
		for i := 1; i <= ncol; i++ {
			cols = append(cols, fmt.Sprint(i))
		}
		order = strings.Join(cols, ", ")
	}
	st, err = db.PrepareOnce("SELECT * FROM " + table + " ORDER BY " + order)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()
	var rows [][]any
	for {
		ok, err := st.Step()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return rows
		}
		row := make([]any, ncol)
		for i := range row {
			row[i] = st.Value(i)
		}
		rows = append(rows, row)
	}
}

func same(a, b any) bool {
	fa, oka := a.(float64)
	fb, okb := b.(float64)
	if oka && okb {
		return math.Float64bits(fa) == math.Float64bits(fb)
	}
	return a == b
}

// compareFiles checks that two files hold the same rows, bit for bit. It returns the rows compared.
func compareFiles(t *testing.T, want, got *sqlite.DB) int {
	t.Helper()
	wt, gt := tables(t, want), tables(t, got)
	if len(wt) != len(gt) {
		t.Fatalf("tables differ: %v and %v", wt, gt)
	}
	total := 0
	for name, noRowid := range wt {
		a, b := dump(t, want, name, noRowid), dump(t, got, name, noRowid)
		total += len(a)
		if len(a) != len(b) {
			t.Errorf("%s: %d rows from SQL, %d from the Engine", name, len(a), len(b))
			continue
		}
		bad := 0
		for i := range a {
			for j := range a[i] {
				if !same(a[i][j], b[i][j]) {
					if bad < 3 {
						t.Errorf("%s row %d column %d: SQL %v (%T), Engine %v (%T)", name, i, j, a[i][j], a[i][j], b[i][j], b[i][j])
					}
					bad++
				}
			}
		}
		if bad > 0 {
			t.Errorf("%s: %d values differ", name, bad)
		}
	}
	return total
}

func memStore(t *testing.T) *sqlitestore.Store {
	st, err := sqlitestore.Open(":memory:", sqlitestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestLiterals(t *testing.T) {
	for _, name := range []string{"latency.precompute", "trades.precompute"} {
		pol := readPolicy(t, name)
		lay, err := compile.NewLayout(pol, name)
		if err != nil {
			t.Fatal(err)
		}
		st := memStore(t)
		for _, sl := range lay.Streams {
			for what, x := range map[string]float64{"ln gamma": sl.LnGamma, "alpha": sl.Alpha, "z2": sl.Z2, "huber": sl.Huber, "clip": sl.Clip, "gamma": sl.Gamma} {
				if x == 0 {
					continue
				}
				lit := compileLiteral(x)
				var got float64
				st.Query("SELECT "+lit, nil, func(row []any) error { got = row[0].(float64); return nil })
				if math.Float64bits(got) != math.Float64bits(x) {
					t.Errorf("%s %s: SQLite reads %s as %v, Go has %v", name, what, lit, got, x)
				}
			}
		}
		st.Close()
	}
}

// compileLiteral writes a double the way the compiler does.
func compileLiteral(x float64) string {
	s := fmt.Sprintf("%v", x)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

func TestEngineMatchesSQL(t *testing.T) {
	cases := []struct {
		policy, stream string
		events         []event
	}{
		{"latency.precompute", "latency", latencyEvents(150000, 1)},
		{"trades.precompute", "trades", tradesEvents(150000, 2)},
	}
	for _, c := range cases {
		t.Run(c.stream, func(t *testing.T) {
			pol := readPolicy(t, c.policy)
			sqlStore := memStore(t)
			defer sqlStore.Close()
			now := sqlRuntime(t, sqlStore.DB(), pol, c.policy, c.stream, c.events, 7919)

			st := memStore(t)
			defer st.Close()
			eng, err := engine.Open(st, readPolicy(t, c.policy), c.policy)
			if err != nil {
				t.Fatal(err)
			}
			runEngine(t, eng, c.stream, c.events, 0, 3)
			if eng.Now() != now {
				t.Fatalf("newest event %d, SQL saw %d", eng.Now(), now)
			}
			rows := compareFiles(t, sqlStore.DB(), st.DB())
			s := eng.Stats()
			t.Logf("%d events, %d rows compared, %d checkpoints, %d rows written, %d reads from the file, %d windows in memory",
				s.Events, rows, s.Checkpoints, s.RowsWritten, s.Reads, eng.Resident())
		})
	}
}
