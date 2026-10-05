//go:build cgo

package sqlitestore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/engine/sqlitestore"
)

// TestSpeed reports events per second for the Engine and for the triggers on the same events.
// Run with SPEED=1 go test -run TestSpeed -v ./engine/sqlitestore.
func TestSpeed(t *testing.T) {
	if os.Getenv("SPEED") == "" {
		t.Skip("set SPEED=1 to measure")
	}
	evs := tradesEvents(1000000, 21)
	pol := readPolicy(t, "trades.precompute")
	for _, sync := range []string{"off", "normal", "full"} {
		st, err := sqlitestore.Open(filepath.Join(t.TempDir(), "e.db"), sqlitestore.Options{Sync: sync})
		if err != nil {
			t.Fatal(err)
		}
		eng, err := engine.Open(st, readPolicy(t, "trades.precompute"), "trades.precompute")
		if err != nil {
			t.Fatal(err)
		}
		si := eng.Stream("trades")
		keys := map[string]int32{}
		for _, e := range evs {
			if _, ok := keys[e.keys[0].(string)]; !ok {
				keys[e.keys[0].(string)], _ = eng.Key(si, e.keys...)
			}
		}
		start := time.Now()
		var ck time.Duration
		for i, e := range evs {
			if err := eng.Put(si, "feed", int64(i+1), e.ts, keys[e.keys[0].(string)], e.vals); err != nil {
				t.Fatal(err)
			}
			if (i+1)%20000 == 0 {
				c := time.Now()
				if err := eng.Checkpoint(); err != nil {
					t.Fatal(err)
				}
				ck += time.Since(c)
			}
		}
		c := time.Now()
		eng.Checkpoint()
		ck += time.Since(c)
		el := time.Since(start)
		t.Logf("engine, sync %-6s: %d events in %v: %.0f events/s (checkpoints %v of it)", sync, len(evs), el.Round(time.Millisecond), float64(len(evs))/el.Seconds(), ck.Round(time.Millisecond))
		st.Close()
	}
	sqlStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "s.db"), sqlitestore.Options{Sync: "off"})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlStore.Close()
	start := time.Now()
	sqlRuntime(t, sqlStore.DB(), pol, "trades.precompute", "trades", evs, 20000)
	el := time.Since(start)
	t.Logf("triggers, one transaction: %d events in %v: %.0f events/s", len(evs), el.Round(time.Millisecond), float64(len(evs))/el.Seconds())
}
