//go:build cgo

package sqlitestore_test

import (
	"errors"
	"math/rand"
	"path/filepath"
	"testing"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/engine/sqlitestore"
)

// failing is a store whose checkpoints fail part way through, after some of their rows are written,
// the way a disk error or a kill in the middle of a transaction would stop them.
type failing struct {
	*sqlitestore.Store
	fail bool
}

func (f *failing) Apply(b *engine.Batch) error {
	if !f.fail || len(b.Blocks) < 2 {
		return f.Store.Apply(b)
	}
	bad := *b
	half := len(b.Blocks) / 2
	bad.Blocks = append(append(append([]*engine.Block{}, b.Blocks[:half]...),
		&engine.Block{SQL: "INSERT INTO no_such_table (x) VALUES (?)", Types: []byte{'i'}, Ints: []int64{1}, N: 1}), b.Blocks[half:]...)
	if err := f.Store.Apply(&bad); err == nil {
		return errors.New("the broken checkpoint was expected to fail")
	}
	return errors.New("checkpoint failed")
}

// TestCrashes stops the Engine many times without warning: sometimes between checkpoints, sometimes
// in the middle of one. Each time a new Engine opens the file and the sender resends everything
// after the last sequence number the file holds. The file must end up exactly as if nothing had
// happened.
func TestCrashes(t *testing.T) {
	for _, c := range []struct {
		policy, stream string
		events         []event
	}{
		{"trades.precompute", "trades", tradesEvents(120000, 5)},
		{"latency.precompute", "latency", latencyEvents(120000, 6)},
		{"usage.precompute", "usage", usageEvents(120000, 7)},
	} {
		t.Run(c.stream, func(t *testing.T) {
			sqlStore := memStore(t)
			defer sqlStore.Close()
			sqlRuntime(t, sqlStore.DB(), readPolicy(t, c.policy), c.policy, c.stream, c.events, 5003)

			path := filepath.Join(t.TempDir(), "engine.db")
			rnd := rand.New(rand.NewSource(9))
			crashes, midCheckpoint, resent := 0, 0, 0
			for first := true; ; first = false {
				st, err := sqlitestore.Open(path, sqlitestore.Options{Sync: "off"})
				if err != nil {
					t.Fatal(err)
				}
				pol := readPolicy(t, c.policy)
				if !first {
					pol = nil // the file carries its policy
				}
				store := &failing{Store: st}
				eng, err := engine.Open(store, pol, c.policy)
				if err != nil {
					t.Fatal(err)
				}
				si := eng.Stream(c.stream)
				from := int(eng.Committed("feed"))
				// Run a while, then stop without warning unless the events run out.
				stopAt := from + 2000 + rnd.Intn(15000)
				nextCk := from + 200 + rnd.Intn(3000)
				done := false
				for i := from; ; i++ {
					if i == len(c.events) {
						if err := eng.Checkpoint(); err != nil {
							t.Fatal(err)
						}
						done = true
						break
					}
					if i == stopAt {
						break
					}
					e := c.events[i]
					key, _ := eng.Key(si, e.keys...)
					if err := eng.PutID(si, "feed", int64(i+1), e.ts, e.id, key, e.vals); err != nil {
						t.Fatal(err)
					}
					if i == nextCk {
						// One checkpoint in five fails half written; the Engine is then dropped.
						if rnd.Intn(5) == 0 {
							store.fail = true
							if err := eng.Checkpoint(); err == nil {
								t.Fatal("expected the checkpoint to fail")
							}
							midCheckpoint++
							break
						}
						if err := eng.Checkpoint(); err != nil {
							t.Fatal(err)
						}
						nextCk = i + 200 + rnd.Intn(3000)
					}
				}
				if !done {
					crashes++
					resent += int(eng.Applied("feed") - eng.Committed("feed"))
				}
				st.Close() // no checkpoint: whatever was only in memory is gone
				if done {
					break
				}
			}
			final, err := sqlitestore.Open(path, sqlitestore.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer final.Close()
			rows := compareFiles(t, sqlStore.DB(), final.DB())
			var seq, events int64
			final.Query("SELECT seq, events FROM "+engine.SourcesTable+" WHERE source = 'feed'", nil, func(r []any) error {
				seq, events = r[0].(int64), r[1].(int64)
				return nil
			})
			if seq != int64(len(c.events)) || events != int64(len(c.events)) {
				t.Errorf("sources: seq %d, events %d, want %d", seq, events, len(c.events))
			}
			t.Logf("%d stops (%d in the middle of a checkpoint), %d events resent, %d rows identical to the SQL runtime's file", crashes, midCheckpoint, resent, rows)
		})
	}
}

// TestHandOver runs half the events through the triggers and half through the Engine on the same
// file, and the other way round. Both files must equal the file the triggers make from all events.
func TestHandOver(t *testing.T) {
	evs := tradesEvents(60000, 8)
	half := len(evs) / 2
	whole := memStore(t)
	defer whole.Close()
	sqlRuntime(t, whole.DB(), readPolicy(t, "trades.precompute"), "trades.precompute", "trades", evs, 4001)

	t.Run("sql then engine", func(t *testing.T) {
		st := memStore(t)
		defer st.Close()
		sqlRuntime(t, st.DB(), readPolicy(t, "trades.precompute"), "trades.precompute", "trades", evs[:half], 4001)
		eng, err := engine.Open(st, nil, "trades.precompute")
		if err != nil {
			t.Fatal(err)
		}
		runEngine(t, eng, "trades", evs, half, 11)
		compareFiles(t, whole.DB(), st.DB())
	})
	t.Run("engine then sql", func(t *testing.T) {
		st := memStore(t)
		defer st.Close()
		eng, err := engine.Open(st, readPolicy(t, "trades.precompute"), "trades.precompute")
		if err != nil {
			t.Fatal(err)
		}
		runEngine(t, eng, "trades", evs[:half], 0, 12)
		// The triggers are in the Engine's file too: carry on with plain INSERTs.
		sqlContinue(t, st, "trades", evs[half:], 4001)
		compareFiles(t, whole.DB(), st.DB())
	})
}
