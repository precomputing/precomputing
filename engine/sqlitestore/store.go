//go:build cgo

// Package sqlitestore keeps an Engine's file on disk with the SQLite compiled into the binary.
package sqlitestore

import (
	"fmt"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/internal/sqlite"
)

// Options tune how the file is written.
type Options struct {
	// Sync is "full" (every checkpoint reaches the disk before it is acknowledged: survives power
	// loss), "normal" (survives a crash of the process, not of the machine) or "off".
	Sync string
}

// Store is an Engine's file on disk.
type Store struct {
	db *sqlite.DB
}

// Open opens or creates the file at path. ":memory:" keeps it in memory.
func Open(path string, o Options) (*Store, error) {
	db, err := sqlite.Open(path)
	if err != nil {
		return nil, err
	}
	sync := o.Sync
	if sync == "" {
		sync = "full"
	}
	if sync != "full" && sync != "normal" && sync != "off" {
		db.Close()
		return nil, fmt.Errorf("sync must be full, normal or off, not %q", sync)
	}
	setup := "PRAGMA synchronous = " + sync + ";"
	if path != ":memory:" {
		setup = "PRAGMA journal_mode = WAL; " + setup
	}
	if err := db.Exec(setup); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// DB is the underlying database, for reading answers with SQL.
func (s *Store) DB() *sqlite.DB { return s.db }

// Close closes the file. Anything not checkpointed is not in it.
func (s *Store) Close() error { return s.db.Close() }

// Exec runs SQL with no parameters.
func (s *Store) Exec(sql string) error { return s.db.Exec(sql) }

// Log is the C library's logarithm, the one SQLite's ln() uses.
func (s *Store) Log(x float64) float64 { return sqlite.Log(x) }

// Query runs one statement and calls fn for each row.
func (s *Store) Query(sql string, args []any, fn func(row []any) error) error {
	st, err := s.db.Prepare(sql)
	if err != nil {
		return err
	}
	defer st.Reset()
	for i, a := range args {
		if err := st.Bind(i+1, a); err != nil {
			return err
		}
	}
	n := st.Columns()
	row := make([]any, n)
	for {
		ok, err := st.Step()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		for i := range row {
			row[i] = st.Value(i)
		}
		if fn != nil {
			if err := fn(row); err != nil {
				return err
			}
		}
	}
}

// Apply writes a checkpoint in one transaction.
func (s *Store) Apply(b *engine.Batch) (err error) {
	if err := s.db.Exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.db.Exec("ROLLBACK")
		}
	}()
	for _, blk := range b.Blocks {
		st, err := s.db.Prepare(blk.SQL)
		if err != nil {
			return err
		}
		if err := st.RunRows(&sqlite.Rows{Types: blk.Types, Ints: blk.Ints, Reals: blk.Reals,
			Text: blk.Text, Offs: blk.Offs, Lens: blk.Lens, N: blk.N}); err != nil {
			return fmt.Errorf("%v in %s", err, blk.SQL)
		}
	}
	for _, sql := range b.Distill {
		st, err := s.db.Prepare(sql)
		if err != nil {
			return err
		}
		if i := st.ParamIndex(":now"); i > 0 {
			if err := st.Bind(i, b.Now); err != nil {
				return err
			}
		}
		if _, err := st.Step(); err != nil {
			st.Reset()
			return err
		}
		st.Reset()
	}
	return s.db.Exec("COMMIT")
}
