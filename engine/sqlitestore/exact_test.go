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
