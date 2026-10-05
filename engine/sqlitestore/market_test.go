//go:build cgo

package sqlitestore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/engine/sqlitestore"
	"precomputing.com/precomputing/internal/market"
)

func marketEvents(seconds int) []event {
	m := market.New()
	var out []event
	for t := 0; t < seconds; t++ {
		o := m.Next()
		for i := range o.Sym {
			out = append(out, event{ts: o.TS, keys: []any{market.Symbols[o.Sym[i]]}, vals: []float64{o.Price[i], o.Size[i]}})
		}
	}
	return out
}

// TestMarketDay runs the Demo 2 day through the Engine on disk, and its first half hour through
// the triggers as well, and checks both files agree on that half hour.
func TestMarketDay(t *testing.T) {
	if testing.Short() || os.Getenv("MARKET") == "" {
		t.Skip("set MARKET=1 to run the whole trading day")
	}
	evs := marketEvents(market.Day)
	dir := t.TempDir()
	st, err := sqlitestore.Open(filepath.Join(dir, "engine.db"), sqlitestore.Options{Sync: "full"})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Open(st, readPolicy(t, "trades.precompute"), "trades.precompute")
	if err != nil {
		t.Fatal(err)
	}
	si := eng.Stream("trades")
	keys := map[string]int32{}
	for _, s := range market.Symbols {
		keys[s], _ = eng.Key(si, s)
	}
	start := time.Now()
	var ck time.Duration
	for i, e := range evs {
		if err := eng.Put(si, "feed", int64(i+1), e.ts, keys[e.keys[0].(string)], e.vals); err != nil {
			t.Fatal(err)
		}
		if (i+1)%10000 == 0 {
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
	s := eng.Stats()
	fi, _ := os.Stat(filepath.Join(dir, "engine.db"))
	t.Logf("engine: %d trades in %v = %.0f trades/s; %d checkpoints taking %v; %d rows written; file %d bytes",
		len(evs), el.Round(time.Millisecond), float64(len(evs))/el.Seconds(), s.Checkpoints, ck.Round(time.Millisecond), s.RowsWritten, fi.Size())
	st.Close()

	half := 0
	for half < len(evs) && evs[half].ts < market.Open+1800 {
		half++
	}
	sqlStore, _ := sqlitestore.Open(filepath.Join(dir, "sql.db"), sqlitestore.Options{Sync: "off"})
	defer sqlStore.Close()
	start = time.Now()
	sqlRuntime(t, sqlStore.DB(), readPolicy(t, "trades.precompute"), "trades.precompute", "trades", evs[:half], 5000)
	el = time.Since(start)
	t.Logf("triggers: %d trades in %v = %.0f trades/s", half, el.Round(time.Millisecond), float64(half)/el.Seconds())

	st2 := memStore(t)
	defer st2.Close()
	eng2, _ := engine.Open(st2, readPolicy(t, "trades.precompute"), "trades.precompute")
	runEngine(t, eng2, "trades", evs[:half], 0, 5)
	rows := compareFiles(t, sqlStore.DB(), st2.DB())
	t.Logf("first half hour: %d rows identical", rows)
}
