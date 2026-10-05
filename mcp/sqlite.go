//go:build cgo

package mcp

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"precomputing.com/precomputing/internal/sqlite"
)

// SQLiteReader reads a file through one SQLite connection, which should be opened read-only.
// It is safe for concurrent use: reads take turns. A read that runs longer than Timeout is
// stopped.
type SQLiteReader struct {
	DB      *sqlite.DB
	Timeout time.Duration // 0 means 5 seconds

	mu     sync.Mutex // one read at a time on the connection
	imu    sync.Mutex // guards gen and active, for the timer
	gen    uint64
	active bool
}

// Read runs one read-only statement.
func (r *SQLiteReader) Read(sql string, args []any, max int) (*Rows, error) {
	if err := singleStatement(sql); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.DB.PrepareSingle(sql)
	if err != nil {
		return nil, err
	}
	defer st.Finalize()
	if !st.ReadOnly() {
		return nil, errors.New("only reading is allowed; the file changes through its streams")
	}
	for i, a := range args {
		if err := st.Bind(i+1, a); err != nil {
			return nil, err
		}
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	r.imu.Lock()
	r.gen++
	gen := r.gen
	r.active = true
	r.imu.Unlock()
	timer := time.AfterFunc(timeout, func() {
		r.imu.Lock()
		if r.active && r.gen == gen {
			r.DB.Interrupt()
		}
		r.imu.Unlock()
	})
	defer func() {
		r.imu.Lock()
		r.active = false
		r.imu.Unlock()
		timer.Stop()
	}()
	out := &Rows{}
	for i := 0; i < st.Columns(); i++ {
		out.Columns = append(out.Columns, st.ColumnName(i))
	}
	for {
		ok, err := st.Step()
		if err != nil {
			var se *sqlite.Error
			if errors.As(err, &se) && se.Code&0xff == 9 { // SQLITE_INTERRUPT
				return nil, fmt.Errorf("the read took longer than %s and was stopped; narrow it", timeout)
			}
			return nil, err
		}
		if !ok {
			return out, nil
		}
		if max > 0 && len(out.Rows) == max {
			out.More = true
			return out, nil
		}
		row := make([]any, len(out.Columns))
		for i := range row {
			row[i] = st.Value(i)
		}
		out.Rows = append(out.Rows, row)
	}
}
