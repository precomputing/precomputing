//go:build cgo

package sqlitestore_test

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/policy"
)

// usageEvents: AI requests over six weeks from September 1st, with retries (the same id again
// soon after), requests up to 30 hours late, and the September close in the middle. Planted events
// are refused as closed (about a day late, their month closed) or late (more than three days).
func usageEvents(n int, seed int64) []event {
	rnd := rand.New(rand.NewSource(seed))
	customers := []string{"acme", "birch", "cobalt", "dune"}
	models := []string{"small", "medium", "large"}
	gateways := []string{"eu", "us"}
	ts := int64(1788220800) // 2026-09-01 00:00 UTC
	var out []event
	planted := false
	for i := 0; i < n; i++ {
		ts += int64(rnd.Intn(180))
		at := ts
		if rnd.Intn(250) == 0 {
			at -= int64(rnd.Intn(30 * 3600)) // late: some within a day, some beyond
		}
		e := event{ts: at, id: fmt.Sprintf("q%d", i),
			keys: []any{customers[rnd.Intn(len(customers))], models[rnd.Intn(len(models))], gateways[rnd.Intn(len(gateways))]},
			vals: []float64{float64(20 + rnd.Intn(4000)), float64(10 + rnd.Intn(1500))}}
		out = append(out, e)
		if rnd.Intn(60) == 0 && len(out) > 3 {
			out = append(out, out[len(out)-1-rnd.Intn(3)]) // a retry
		}
		// Planted once September has closed (October 2nd): requests from late on September 30th,
		// about a day late with their month closed, and requests more than three days late.
		if !planted && ts > 1790812800+86400+3600 {
			planted = true
			for k := 0; k < 5; k++ {
				out = append(out, event{ts: 1790798400 + int64(k)*1800, id: fmt.Sprintf("sep30-%d", k),
					keys: []any{customers[k%4], "small", "eu"}, vals: []float64{100, 50}})
			}
			for k := 0; k < 3; k++ {
				out = append(out, event{ts: ts - 80*3600, id: fmt.Sprintf("old-%d", k),
					keys: []any{customers[k%4], "large", "us"}, vals: []float64{100, 50}})
			}
		}
	}
	return out
}

func TestExactMatchesSQL(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("..", "..", "examples", "usage.precompute"))
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"usage":             string(base),
		"late within close": strings.Replace(string(base), "late   48h", "late   24h", 1),
	} {
		t.Run(name, func(t *testing.T) {
			pol, err := policy.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			evs := usageEvents(60000, 4)
			sqlStore := memStore(t)
			defer sqlStore.Close()
			sqlRuntime(t, sqlStore.DB(), pol, "usage.precompute", "usage", evs, 4999)
			st := memStore(t)
			defer st.Close()
			pol2, _ := policy.Parse(src)
			eng, err := engine.Open(st, pol2, "usage.precompute")
			if err != nil {
				t.Fatal(err)
			}
			runEngine(t, eng, "usage", evs, 0, 6)
			rows := compareFiles(t, sqlStore.DB(), st.DB())
			var refused string
			st.Query("SELECT group_concat(reason || ' ' || total, ', ') FROM (SELECT reason, sum(n) AS total FROM usage_refused GROUP BY reason)", nil, func(r []any) error {
				refused, _ = r[0].(string)
				return nil
			})
			t.Logf("%d events, %d rows identical; refused: %s; the Engine counted %d refusals", len(evs), rows, refused, eng.Stats().Refused)
			if !strings.Contains(refused, "repeat") || !strings.Contains(refused, "late") {
				t.Errorf("the test should exercise repeats and late events: %s", refused)
			}
			if name == "usage" && !strings.Contains(refused, "closed") {
				t.Errorf("with late beyond the close, some events should be refused as closed: %s", refused)
			}
		})
	}
}

// TestAheadMatchesSQL: with ahead, an event stamped more than that past the wall clock is refused
// as ahead by both runtimes, before any other check, and moves no clock. Without the rule, the same
// events close September early, from the planted one on, and every later report is refused.
func TestAheadMatchesSQL(t *testing.T) {
	base, err := os.ReadFile(filepath.Join("..", "..", "examples", "usage.precompute"))
	if err != nil {
		t.Fatal(err)
	}
	src := strings.Replace(string(base), "late   48h", "late   48h\n  ahead  1h", 1)
	evs := usageEvents(20000, 7)
	// A gateway whose clock is years ahead sends a few reports in the middle of the run, one of them
	// a repeat of an earlier id: ahead comes first, so it is counted as ahead, not as a repeat.
	far := int64(4102444800) // 2100-01-01
	mid := len(evs) / 2
	planted := []event{
		{ts: far, id: "future-1", keys: []any{"acme", "small", "eu"}, vals: []float64{100, 50}},
		{ts: far + 3600, id: "future-2", keys: []any{"birch", "large", "us"}, vals: []float64{100, 50}},
		{ts: far, id: evs[3].id, keys: evs[3].keys, vals: evs[3].vals},
	}
	evs = append(evs[:mid], append(planted, evs[mid:]...)...)
	refusedBy := func(db interface {
		Query(string, []any, func([]any) error) error
	}) map[string]int64 {
		out := map[string]int64{}
		db.Query("SELECT reason, sum(n) FROM usage_refused GROUP BY reason", nil, func(r []any) error {
			out[r[0].(string)] = r[1].(int64)
			return nil
		})
		return out
	}

	pol, err := policy.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	sqlStore := memStore(t)
	defer sqlStore.Close()
	sqlRuntime(t, sqlStore.DB(), pol, "usage.precompute", "usage", evs, 4999)
	st := memStore(t)
	defer st.Close()
	pol2, _ := policy.Parse(src)
	eng, err := engine.Open(st, pol2, "usage.precompute")
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, eng, "usage", evs, 0, 8)
	rows := compareFiles(t, sqlStore.DB(), st.DB())
	got := refusedBy(st)
	if got["ahead"] != 3 {
		t.Errorf("refused as ahead: %d, want the 3 planted reports (all refusals: %v)", got["ahead"], got)
	}
	if eng.Now() >= far {
		t.Errorf("the Engine's clock moved to a refused time: %d", eng.Now())
	}
	var newest int64
	st.Query("SELECT newest FROM usage_clock", nil, func(r []any) error { newest = r[0].(int64); return nil })
	if newest >= far {
		t.Errorf("usage_clock.newest moved to a refused time: %d", newest)
	}
	t.Logf("%d events, %d rows identical; refused: %v", len(evs), rows, got)

	// The same reports without the rule: the first planted one moves the clock to 2100 and every
	// report after it is refused, which is what ahead is for.
	plain := memStore(t)
	defer plain.Close()
	pol3, _ := policy.Parse(string(base))
	sqlRuntime(t, plain.DB(), pol3, "usage.precompute", "usage", evs, 4999)
	without := refusedBy(plain)
	if without["closed"]+without["late"] < int64(len(evs)-mid-len(planted)) {
		t.Errorf("without ahead, the reports after the planted one should be refused: %v", without)
	}
}

// TestAheadWallClock: the Engine compares with its wall clock, which tests and replays may set.
func TestAheadWallClock(t *testing.T) {
	src := `stream usage exact {
  id     request_id refuse repeats 7d
  key    customer text
  value  tokens integer
  ahead  10m
  period day close 1h
  raw    until closed + 1d
  rollup 1h keep 30d
}`
	pol, err := policy.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	st := memStore(t)
	defer st.Close()
	eng, err := engine.Open(st, pol, "t")
	if err != nil {
		t.Fatal(err)
	}
	now := int64(1790000000)
	eng.SetWall(func() int64 { return now })
	s := eng.Stream("usage")
	key, _ := eng.Key(s, "acme")
	for i, ts := range []int64{now, now + 600, now + 601, now + 7200} {
		if err := eng.PutID(s, "a", int64(i+1), ts, fmt.Sprintf("r%d", i), key, []float64{5}); err != nil {
			t.Fatal(err)
		}
	}
	if got := eng.Stats().Refused; got != 2 {
		t.Errorf("refused %d, want 2: at most 10 minutes ahead is counted", got)
	}
	if eng.Now() != now+600 {
		t.Errorf("clock %d, want %d", eng.Now(), now+600)
	}
	now += 7200 // the wall clock catches up: the same time is now counted
	if err := eng.PutID(s, "a", 5, now, "r9", key, []float64{5}); err != nil {
		t.Fatal(err)
	}
	if got := eng.Stats().Refused; got != 2 {
		t.Errorf("refused %d after the clock caught up, want still 2", got)
	}
}
