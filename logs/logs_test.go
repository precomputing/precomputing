//go:build cgo

package logs

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/engine/sqlitestore"
	"precomputing.com/precomputing/internal/filecmp"
	"precomputing.com/precomputing/internal/sqlite"
	"precomputing.com/precomputing/policy"
)

// shopLines makes n log lines of a small web shop over two hours, with a payment incident that
// brings a new error in the second hour, a few lines that do not match the format and one with
// a time that does not read.
func shopLines(n int, seed int64) []string {
	rnd := rand.New(rand.NewSource(seed))
	start := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	routes := []string{"/", "/search", "/product/:id", "/cart", "/checkout", "/login"}
	providers := []string{"northpay", "quickcard", "banklink"}
	var out []string
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * 2 * time.Hour / time.Duration(n)).Add(time.Duration(rnd.Intn(900)) * time.Millisecond)
		ts := at.Format("2006-01-02T15:04:05.000Z")
		incident := i > n*6/10 && i < n*7/10
		switch k := rnd.Intn(100); {
		case k < 55:
			r := routes[rnd.Intn(len(routes))]
			path := strings.Replace(r, ":id", fmt.Sprint(1000+rnd.Intn(9000)), 1)
			ms := 5 + rnd.ExpFloat64()*40
			if rnd.Intn(500) == 0 {
				ms *= 30
			}
			out = append(out, fmt.Sprintf("%s INFO web GET %s route=%s status=200 ms=%.1f bytes=%d", ts, path, r, ms, 800+rnd.Intn(9000)))
		case k < 65:
			p := providers[rnd.Intn(len(providers))]
			switch {
			case incident && p == "northpay":
				out = append(out, fmt.Sprintf("%s ERROR payments provider unavailable provider=%s status=503 attempt=%d", ts, p, 1+rnd.Intn(3)))
			case rnd.Intn(10) == 0:
				out = append(out, fmt.Sprintf("%s WARN payments charge declined provider=%s result=declined code=%d amount=%d.%02d", ts, p, 51+rnd.Intn(3), 5+rnd.Intn(200), rnd.Intn(100)))
			default:
				out = append(out, fmt.Sprintf("%s INFO payments charge ok provider=%s result=ok amount=%d.%02d ms=%d", ts, p, 5+rnd.Intn(200), rnd.Intn(100), 150+rnd.Intn(400)))
			}
		case k < 80:
			out = append(out, fmt.Sprintf(`%s INFO search query q="%s" results=%d ms=%d`, ts, []string{"red shoes", "rain jacket", "tent", "socks"}[rnd.Intn(4)], rnd.Intn(80), 3+rnd.Intn(60)))
		case k < 90:
			out = append(out, fmt.Sprintf("%s INFO checkout order placed order=A-%d items=%d total=%d.%02d session=%08x-%04x", ts, 10000+i, 1+rnd.Intn(5), 10+rnd.Intn(300), rnd.Intn(100), rnd.Uint32(), rnd.Intn(65536)))
		case k < 97:
			out = append(out, fmt.Sprintf("%s INFO login login ok user=u-%d method=password", ts, rnd.Intn(5000)))
		case k < 99:
			out = append(out, fmt.Sprintf("%s ERROR login login failed user=u-%d reason=bad_password", ts, rnd.Intn(5000)))
		default:
			out = append(out, "this line has no time")
		}
	}
	out[n/2] = "2026-09-29T25:61:00.000Z INFO web GET / route=/ status=200 ms=1"
	return out
}

func readPolicy(t *testing.T) *policy.Policy {
	src, err := os.ReadFile(filepath.Join("testdata", "shop.precompute"))
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

func open(t *testing.T, path string, pol *policy.Policy) (*sqlitestore.Store, *engine.Engine, *Reducer) {
	t.Helper()
	st, err := sqlitestore.Open(path, sqlitestore.Options{Sync: "off"})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Open(st, pol, "shop.precompute")
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(eng)
	if err != nil {
		t.Fatal(err)
	}
	return st, eng, r
}

func TestReducer(t *testing.T) {
	lines := shopLines(20000, 1)
	st, eng, r := open(t, ":memory:", readPolicy(t))
	defer st.Close()
	var incident *Template
	for i, l := range lines {
		res, err := r.Put("app", int64(i+1), l)
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		if res.New && strings.HasPrefix(res.Template.Text(), "provider unavailable") {
			incident = res.Template
		}
		if i%3000 == 0 {
			if err := eng.Checkpoint(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := eng.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	s := r.Stats()
	t.Logf("%d lines, %d not read, %d events, %d templates", s.Lines, s.NotRead, s.Events, len(r.Templates()))
	for _, tp := range r.Templates() {
		t.Logf("  %2d %-9s %6d  %s", tp.ID, tp.Service, tp.Count(), tp.Text())
	}
	if incident == nil || incident.Text() != "provider unavailable provider=<*> status=<*> attempt=<*>" {
		t.Fatalf("the incident's error should be a template of its own: %v", incident)
	}
	if want := "query q=<*> results=<*> ms=<*>"; !hasTemplate(r, "search", want) {
		t.Errorf("no template %q: quoted values should be masked whole", want)
	}
	if !hasTemplate(r, "checkout", "order placed order=<*> items=<*> total=<*> session=<*>") {
		t.Errorf("the checkout template should mask its values")
	}
	var rows, notRead int64
	st.Query("SELECT count(*) FROM lines_raw", nil, func(row []any) error { rows = row[0].(int64); return nil })
	for _, l := range lines {
		if strings.HasPrefix(l, "this line") || strings.Contains(l, "T25:61") {
			notRead++
		}
	}
	if s.NotRead != notRead || rows != int64(len(lines))-notRead {
		t.Errorf("%d lines kept and %d not read; want %d and %d", rows, s.NotRead, int64(len(lines))-notRead, notRead)
	}
	// Every stream got exactly the lines its where and fields select.
	count := func(sql string) (n int64) {
		st.Query(sql, nil, func(row []any) error { n, _ = row[0].(int64); return nil })
		return
	}
	var web, pay, errs int64
	for _, l := range lines {
		switch {
		case strings.Contains(l, "T25:61") || strings.HasPrefix(l, "this"):
		case strings.Contains(l, " INFO web "):
			web++
		case strings.Contains(l, " payments ") && strings.Contains(l, "result="):
			pay++
		}
		if strings.Contains(l, " ERROR ") {
			errs++
		}
	}
	if got := count("SELECT sum(n) FROM web_win WHERE res = 60"); got != web {
		t.Errorf("web: %d events, want %d", got, web)
	}
	if got := count("SELECT sum(n) FROM payments_win WHERE res = 60"); got != pay {
		t.Errorf("payments: %d events, want %d (lines without result= are not payments events)", got, pay)
	}
	if got := count("SELECT sum(n) FROM errors_win WHERE res = 60"); got != errs {
		t.Errorf("errors: %d events, want %d", got, errs)
	}
	if got := count("SELECT count(*) FROM web_anomaly WHERE line LIKE '%INFO web GET%'"); got == 0 {
		t.Errorf("no slow request was kept whole with its line")
	}
	if got := count("SELECT count(*) FROM " + compile.TemplatesTable); got != int64(len(r.Templates())) {
		t.Errorf("%d templates in the file, %d learned", got, len(r.Templates()))
	}
}

func hasTemplate(r *Reducer, service, text string) bool {
	for _, t := range r.Templates() {
		if t.Service == service && t.Text() == text {
			return true
		}
	}
	return false
}

// TestLogsMatchSQL gives the events the reducer makes to the compiled triggers as well, with plain
// INSERTs, and compares the two files table by table, value by value.
func TestLogsMatchSQL(t *testing.T) {
	lines := shopLines(30000, 2)
	pol := readPolicy(t)
	st, eng, r := open(t, ":memory:", pol)
	defer st.Close()
	sqlStore, err := sqlitestore.Open(":memory:", sqlitestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlStore.Close()
	lay, err := compile.NewLayout(pol, "shop.precompute")
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlStore.Exec(lay.Output.Schema); err != nil {
		t.Fatal(err)
	}
	db := sqlStore.DB()
	if err := db.Exec("BEGIN"); err != nil {
		t.Fatal(err)
	}
	stmts := map[string]*sqlite.Stmt{}
	run := func(sql string, args ...any) error {
		st := stmts[sql]
		if st == nil {
			var err error
			if st, err = db.Prepare(sql); err != nil {
				return err
			}
			stmts[sql] = st
		}
		for i, a := range args {
			if err := st.Bind(i+1, a); err != nil {
				return err
			}
		}
		_, err := st.Step()
		st.Reset()
		return err
	}
	for i, l := range lines {
		if _, err := r.Put("app", int64(i+1), l); err != nil {
			t.Fatal(err)
		}
		for _, ev := range r.LastEvents() {
			sl := lay.Streams[ev.Stream]
			s := sl.Stream
			cols := []string{"ts"}
			args := []any{ev.TS}
			for j, k := range s.Keys {
				cols = append(cols, k.Name)
				args = append(args, eng.KeyOf(ev.Stream, ev.Key)[j])
			}
			for j, v := range s.Values {
				cols = append(cols, v.Name)
				if v.Type == "integer" {
					args = append(args, int64(ev.Vals[j]))
				} else {
					args = append(args, ev.Vals[j])
				}
			}
			cols = append(cols, "line")
			args = append(args, ev.Line)
			sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", s.Name, strings.Join(cols, ", "), strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", "))
			if err := run(sql, args...); err != nil {
				t.Fatalf("line %d: %v", i+1, err)
			}
		}
		if i%4000 == 3999 {
			if err := eng.Checkpoint(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := eng.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	for _, d := range lay.Distill {
		if err := run(strings.ReplaceAll(d, ":now", fmt.Sprint(eng.Now()))); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("COMMIT"); err != nil {
		t.Fatal(err)
	}
	tables, err := filecmp.Compare(db, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	rows, values := 0, 0
	for _, tb := range tables {
		if tb.Name == compile.TemplatesTable {
			continue // the Engine learns templates; the triggers take the numbers it gives
		}
		rows += tb.Rows
		values += tb.Values
		if tb.Diffs > 0 {
			t.Errorf("%s: %d differences, first %s", tb.Name, tb.Diffs, tb.First)
		}
	}
	t.Logf("%d lines: %d rows, %d values identical", len(lines), rows, values)
}

// TestLogsCrashes stops the Engine many times without warning, sometimes in the middle of a
// checkpoint; each time a new Engine and reducer open the file and the lines after the last one
// the file holds are sent again. The file must end up exactly as a run that never stopped,
// templates included.
func TestLogsCrashes(t *testing.T) {
	lines := shopLines(25000, 3)
	pol := readPolicy(t)
	dir := t.TempDir()
	whole, weng, wr := open(t, filepath.Join(dir, "whole.db"), pol)
	for i, l := range lines {
		if _, err := wr.Put("app", int64(i+1), l); err != nil {
			t.Fatal(err)
		}
		if i%2500 == 0 {
			weng.Checkpoint()
		}
	}
	if err := weng.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	defer whole.Close()

	path := filepath.Join(dir, "crashed.db")
	rnd := rand.New(rand.NewSource(4))
	stops, resent := 0, 0
	for first := true; ; first = false {
		p := pol
		if !first {
			p = nil
		}
		st, eng, r := open(t, path, p)
		from := int(eng.Committed("app"))
		stopAt := from + 1000 + rnd.Intn(6000)
		next := from + 100 + rnd.Intn(1500)
		done := false
		for i := from; ; i++ {
			if i == len(lines) {
				if err := eng.Checkpoint(); err != nil {
					t.Fatal(err)
				}
				done = true
				break
			}
			if i == stopAt {
				break
			}
			if _, err := r.Put("app", int64(i+1), lines[i]); err != nil {
				t.Fatal(err)
			}
			if i == next {
				if err := eng.Checkpoint(); err != nil {
					t.Fatal(err)
				}
				next = i + 100 + rnd.Intn(1500)
			}
		}
		if !done {
			stops++
			resent += int(eng.Applied("app") - eng.Committed("app"))
		}
		st.Close()
		if done {
			break
		}
	}
	final, err := sqlitestore.Open(path, sqlitestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	tables, err := filecmp.Compare(whole.DB(), final.DB())
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, tb := range tables {
		rows += tb.Rows
		if tb.Diffs > 0 {
			t.Errorf("%s: %d differences, first %s", tb.Name, tb.Diffs, tb.First)
		}
	}
	t.Logf("%d stops, %d lines sent again, %d rows identical to a run that never stopped", stops, resent, rows)
}

func BenchmarkReducer(b *testing.B) {
	lines := shopLines(50000, 5)
	src, _ := os.ReadFile(filepath.Join("testdata", "shop.precompute"))
	pol, _ := policy.Parse(string(src))
	st, _ := sqlitestore.Open(":memory:", sqlitestore.Options{Sync: "off"})
	defer st.Close()
	eng, _ := engine.Open(st, pol, "shop.precompute")
	r, _ := New(eng)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l := lines[i%len(lines)]
		if _, err := r.Put("", 0, l); err != nil {
			b.Fatal(err)
		}
		if i%5000 == 4999 {
			eng.Checkpoint()
		}
	}
}

// TestFastPaths checks that the hand-written splitting and key=value masking read lines exactly as
// the regular expressions they replace.
func TestFastPaths(t *testing.T) {
	src, _ := os.ReadFile(filepath.Join("testdata", "shop.precompute"))
	pol, _ := policy.Parse(string(src))
	st, _ := sqlitestore.Open(":memory:", sqlitestore.Options{})
	defer st.Close()
	eng, _ := engine.Open(st, pol, "shop.precompute")
	r, err := New(eng)
	if err != nil {
		t.Fatal(err)
	}
	if r.simple == 0 {
		t.Fatal("the shop format should take the fast path")
	}
	lines := append(shopLines(3000, 7),
		"2026-09-29T12:00:00Z INFO web a=1 b=\"x y\" c=\"unclosed d=4 =5 _e.f=6 g= h==7",
		"2026-09-29T12:00:00Z\tINFO   web  k=v\tq=\"a=b c=d\"z=1 end",
		"2026-09-29T12:00:00Z INFO web", "2026-09-29T12:00:00Z INFO web ", "2026-09-29T12:00:00Z INFO  web  x",
		"only two", "", "x=1 y=2 z=3 w=4")
	for _, l := range lines {
		text := strings.TrimSpace(l)
		want := r.format.FindStringSubmatch(text)
		got := r.split(text)
		if (want != nil) != got {
			t.Fatalf("%q: regular expression matched %v, split %v", l, want != nil, got)
		}
		if want == nil {
			continue
		}
		for i := 1; i < len(want); i++ {
			if want[i] != r.parts[i] {
				t.Fatalf("%q: field %d is %q, split gives %q", l, i, want[i], r.parts[i])
			}
		}
		msg := want[r.msg]
		wantFields := map[string]string{}
		wantMsg := kvRE.ReplaceAllStringFunc(msg, func(kv string) string {
			lead := ""
			if kv != "" && isSpace(kv[0]) {
				lead, kv = kv[:1], kv[1:]
			}
			eq := strings.IndexByte(kv, '=')
			k, v := kv[:eq], kv[eq+1:]
			if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
				v = v[1 : len(v)-1]
			}
			if _, ok := wantFields[k]; !ok {
				wantFields[k] = v
			}
			return lead + k + "=" + "<*>"
		})
		clear(r.fields)
		gotMsg := r.maskPairs(msg)
		if gotMsg != wantMsg || fmt.Sprint(r.fields) != fmt.Sprint(wantFields) {
			t.Fatalf("%q:\n got  %q %v\n want %q %v", msg, gotMsg, r.fields, wantMsg, wantFields)
		}
	}
}
