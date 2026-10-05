//go:build cgo

// Package sqlite is a small binding to the SQLite amalgamation compiled into the binary.
// It has what the Engine needs and no more: open, run statements, bind rows in bulk, read rows.
package sqlite

/*
#cgo CFLAGS: -O2 -DSQLITE_THREADSAFE=1 -DSQLITE_DEFAULT_MEMSTATUS=0 -DSQLITE_DQS=0
#cgo CFLAGS: -DSQLITE_OMIT_DEPRECATED -DSQLITE_OMIT_SHARED_CACHE -DSQLITE_OMIT_LOAD_EXTENSION
#cgo CFLAGS: -DSQLITE_ENABLE_MATH_FUNCTIONS -DSQLITE_ENABLE_DBSTAT_VTAB -DSQLITE_LIKE_DOESNT_MATCH_BLOBS
#cgo CFLAGS: -DSQLITE_DEFAULT_WAL_SYNCHRONOUS=1 -DHAVE_USLEEP=1 -DSQLITE_USE_ALLOCA
#cgo LDFLAGS: -lm
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include "sqlite3.h"

// pc_run binds and runs one prepared statement for n rows. Parameter types come from types:
// 'i' takes the next int64, 'f' the next double, 't' the next text given by offset and length
// into buf, 'n' binds null. It stops at the first error and returns its code.
static int pc_run(sqlite3_stmt *st, int n, int ncol, const unsigned char *types,
		const long long *ints, const double *reals, const char *buf,
		const int *offs, const int *lens) {
	int ii = 0, fi = 0, ti = 0;
	for (int r = 0; r < n; r++) {
		for (int c = 0; c < ncol; c++) {
			int rc;
			switch (types[c]) {
			case 'i': rc = sqlite3_bind_int64(st, c + 1, ints[ii++]); break;
			case 'f': rc = sqlite3_bind_double(st, c + 1, reals[fi++]); break;
			case 't': rc = sqlite3_bind_text(st, c + 1, buf + offs[ti], lens[ti], SQLITE_STATIC); ti++; break;
			default: rc = sqlite3_bind_null(st, c + 1);
			}
			if (rc != SQLITE_OK) return rc;
		}
		int rc = sqlite3_step(st);
		if (rc != SQLITE_DONE && rc != SQLITE_ROW) {
			sqlite3_reset(st);
			return rc;
		}
		sqlite3_reset(st);
	}
	return SQLITE_OK;
}

static int pc_bind_text(sqlite3_stmt *st, int i, const char *p, int n) {
	return sqlite3_bind_text(st, i, p, n, SQLITE_TRANSIENT);
}

static int pc_open(const char *path, sqlite3 **db) {
	return sqlite3_open_v2(path, db, SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_FULLMUTEX, NULL);
}

static int pc_open_ro(const char *path, sqlite3 **db) {
	return sqlite3_open_v2(path, db, SQLITE_OPEN_READONLY | SQLITE_OPEN_FULLMUTEX, NULL);
}

static double pc_log(double x) { return log(x); }

static void pc_logs(const double *x, double *y, int n) {
	for (int i = 0; i < n; i++) y[i] = log(x[i]);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Version is the SQLite version compiled in.
func Version() string { return C.GoString(C.sqlite3_libversion()) }

// Log is the natural logarithm from the C library SQLite uses for ln(), so values computed in Go
// match the ones SQLite computes in SQL to the last bit.
func Log(x float64) float64 { return float64(C.pc_log(C.double(x))) }

// Logs fills y with the natural logarithm of each x, in one call.
func Logs(x, y []float64) {
	if len(x) == 0 {
		return
	}
	C.pc_logs((*C.double)(unsafe.Pointer(&x[0])), (*C.double)(unsafe.Pointer(&y[0])), C.int(len(x)))
}

// Types of a column value, as SQLite reports them.
const (
	Integer = 1
	Float   = 2
	Text    = 3
	Blob    = 4
	Null    = 5
)

// Error is an SQLite error with its message.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// DB is an open database. It is safe for one goroutine at a time.
type DB struct {
	db    *C.sqlite3
	stmts map[string]*Stmt
}

// Open opens or creates a database file. ":memory:" makes an in-memory one.
func Open(path string) (*DB, error) { return open(path, false) }

// OpenReadOnly opens an existing database for reading.
func OpenReadOnly(path string) (*DB, error) { return open(path, true) }

func open(path string, ro bool) (*DB, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var db *C.sqlite3
	var rc C.int
	if ro {
		rc = C.pc_open_ro(cpath, &db)
	} else {
		rc = C.pc_open(cpath, &db)
	}
	if rc != C.SQLITE_OK {
		msg := "cannot open " + path
		if db != nil {
			msg += ": " + C.GoString(C.sqlite3_errmsg(db))
			C.sqlite3_close_v2(db)
		}
		return nil, &Error{int(rc), msg}
	}
	C.sqlite3_extended_result_codes(db, 1)
	C.sqlite3_busy_timeout(db, 5000)
	return &DB{db: db, stmts: map[string]*Stmt{}}, nil
}

// Close finalizes cached statements and closes the database.
func (d *DB) Close() error {
	if d.db == nil {
		return nil
	}
	for _, s := range d.stmts {
		C.sqlite3_finalize(s.st)
	}
	d.stmts = nil
	rc := C.sqlite3_close_v2(d.db)
	d.db = nil
	if rc != C.SQLITE_OK {
		return &Error{int(rc), "close failed"}
	}
	return nil
}

func (d *DB) err(rc C.int, what string) error {
	return &Error{int(rc), what + ": " + C.GoString(C.sqlite3_errmsg(d.db))}
}

// Exec runs one or more statements with no parameters.
func (d *DB) Exec(sql string) error {
	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))
	var cerr *C.char
	rc := C.sqlite3_exec(d.db, csql, nil, nil, &cerr)
	if rc != C.SQLITE_OK {
		msg := C.GoString(cerr)
		C.sqlite3_free(unsafe.Pointer(cerr))
		return &Error{int(rc), msg}
	}
	return nil
}

// Changes is the number of rows the last statement changed.
func (d *DB) Changes() int64 { return int64(C.sqlite3_changes64(d.db)) }

// Interrupt stops the statements running on the database as soon as it can; they fail with
// SQLITE_INTERRUPT. It may be called from another goroutine. With nothing running it does nothing.
func (d *DB) Interrupt() { C.sqlite3_interrupt(d.db) }

// PrepareSingle compiles exactly one statement, which the caller finalizes. SQL that holds more
// than one statement is refused.
func (d *DB) PrepareSingle(sql string) (*Stmt, error) {
	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))
	var st *C.sqlite3_stmt
	var tail *C.char
	rc := C.sqlite3_prepare_v3(d.db, csql, C.int(len(sql)), 0, &st, &tail)
	if rc != C.SQLITE_OK {
		return nil, d.err(rc, "prepare")
	}
	if st == nil {
		return nil, errors.New("there is no statement to run")
	}
	// What follows the statement may only be white space, semicolons and comments.
	used := int(uintptr(unsafe.Pointer(tail)) - uintptr(unsafe.Pointer(csql)))
	if rest := sql[used:]; rest != "" {
		crest := C.CString(rest)
		var more *C.sqlite3_stmt
		rc := C.sqlite3_prepare_v3(d.db, crest, C.int(len(rest)), 0, &more, nil)
		C.free(unsafe.Pointer(crest))
		if more != nil {
			C.sqlite3_finalize(more)
		}
		if rc != C.SQLITE_OK || more != nil {
			C.sqlite3_finalize(st)
			return nil, errors.New("one statement at a time, please")
		}
	}
	return &Stmt{d: d, st: st, n: int(C.sqlite3_bind_parameter_count(st))}, nil
}

// Stmt is a prepared statement.
type Stmt struct {
	d  *DB
	st *C.sqlite3_stmt
	n  int // parameters
}

// Prepare compiles one statement. Prepared statements are cached by their text and closed with the database.
func (d *DB) Prepare(sql string) (*Stmt, error) {
	if s, ok := d.stmts[sql]; ok {
		return s, nil
	}
	s, err := d.PrepareOnce(sql)
	if err != nil {
		return nil, err
	}
	d.stmts[sql] = s
	return s, nil
}

// PrepareOnce compiles one statement that the caller finalizes.
func (d *DB) PrepareOnce(sql string) (*Stmt, error) {
	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))
	var st *C.sqlite3_stmt
	var tail *C.char
	rc := C.sqlite3_prepare_v3(d.db, csql, C.int(len(sql)), C.SQLITE_PREPARE_PERSISTENT, &st, &tail)
	if rc != C.SQLITE_OK {
		return nil, d.err(rc, "prepare")
	}
	if st == nil {
		return nil, errors.New("prepare: no statement in " + sql)
	}
	return &Stmt{d: d, st: st, n: int(C.sqlite3_bind_parameter_count(st))}, nil
}

// Finalize releases a statement made with PrepareOnce.
func (s *Stmt) Finalize() {
	if s.st != nil {
		C.sqlite3_finalize(s.st)
		s.st = nil
	}
}

// ReadOnly reports whether the statement only reads.
func (s *Stmt) ReadOnly() bool { return C.sqlite3_stmt_readonly(s.st) != 0 }

// Params is the number of parameters.
func (s *Stmt) Params() int { return s.n }

// ParamIndex returns the index of a named parameter such as ":now", or 0.
func (s *Stmt) ParamIndex(name string) int {
	c := C.CString(name)
	defer C.free(unsafe.Pointer(c))
	return int(C.sqlite3_bind_parameter_index(s.st, c))
}

// Rows is a block of parameter rows for RunRows: each row binds Types in order,
// taking integers, reals and texts from their slices in turn.
type Rows struct {
	Types []byte // 'i', 'f', 't' or 'n' per parameter
	Ints  []int64
	Reals []float64
	Text  []byte // all text parameters, one after another
	Offs  []int32
	Lens  []int32
	N     int
}

// RunRows binds and steps the statement once per row, in one call into C.
func (s *Stmt) RunRows(r *Rows) error {
	if r.N == 0 {
		return nil
	}
	if len(r.Types) != s.n {
		return fmt.Errorf("statement takes %d parameters, rows have %d", s.n, len(r.Types))
	}
	// The slices hold no Go pointers, so passing them for the length of the call is allowed.
	ptr := func(b []byte) *C.uchar {
		if len(b) == 0 {
			return nil
		}
		return (*C.uchar)(unsafe.Pointer(&b[0]))
	}
	var ints *C.longlong
	if len(r.Ints) > 0 {
		ints = (*C.longlong)(unsafe.Pointer(&r.Ints[0]))
	}
	var reals *C.double
	if len(r.Reals) > 0 {
		reals = (*C.double)(unsafe.Pointer(&r.Reals[0]))
	}
	var offs, lens *C.int
	if len(r.Offs) > 0 {
		offs = (*C.int)(unsafe.Pointer(&r.Offs[0]))
		lens = (*C.int)(unsafe.Pointer(&r.Lens[0]))
	}
	rc := C.pc_run(s.st, C.int(r.N), C.int(len(r.Types)), ptr(r.Types), ints, reals,
		(*C.char)(unsafe.Pointer(ptr(r.Text))), offs, lens)
	if rc != C.SQLITE_OK {
		return s.d.err(rc, "write")
	}
	return nil
}

// Bind sets parameter i (from 1) to v: nil, int, int64, float64, string or bool.
func (s *Stmt) Bind(i int, v any) error {
	var rc C.int
	switch x := v.(type) {
	case nil:
		rc = C.sqlite3_bind_null(s.st, C.int(i))
	case int:
		rc = C.sqlite3_bind_int64(s.st, C.int(i), C.sqlite3_int64(x))
	case int64:
		rc = C.sqlite3_bind_int64(s.st, C.int(i), C.sqlite3_int64(x))
	case float64:
		rc = C.sqlite3_bind_double(s.st, C.int(i), C.double(x))
	case bool:
		b := 0
		if x {
			b = 1
		}
		rc = C.sqlite3_bind_int64(s.st, C.int(i), C.sqlite3_int64(b))
	case string:
		c := C.CString(x)
		rc = C.pc_bind_text(s.st, C.int(i), c, C.int(len(x)))
		C.free(unsafe.Pointer(c))
	default:
		return fmt.Errorf("cannot bind %T", v)
	}
	if rc != C.SQLITE_OK {
		return s.d.err(rc, "bind")
	}
	return nil
}

// Step runs the statement one step. It returns true while there is a row to read.
func (s *Stmt) Step() (bool, error) {
	rc := C.sqlite3_step(s.st)
	switch rc {
	case C.SQLITE_ROW:
		return true, nil
	case C.SQLITE_DONE:
		return false, nil
	}
	err := s.d.err(rc, "step")
	C.sqlite3_reset(s.st)
	return false, err
}

// Reset makes the statement ready to run again and clears its parameters.
func (s *Stmt) Reset() {
	C.sqlite3_reset(s.st)
	C.sqlite3_clear_bindings(s.st)
}

// Columns is the number of result columns.
func (s *Stmt) Columns() int { return int(C.sqlite3_column_count(s.st)) }

// ColumnName is the name of result column i (from 0).
func (s *Stmt) ColumnName(i int) string { return C.GoString(C.sqlite3_column_name(s.st, C.int(i))) }

// Type is the type of result column i in the current row.
func (s *Stmt) Type(i int) int { return int(C.sqlite3_column_type(s.st, C.int(i))) }

// Int64 reads result column i as an integer.
func (s *Stmt) Int64(i int) int64 { return int64(C.sqlite3_column_int64(s.st, C.int(i))) }

// Float reads result column i as a double.
func (s *Stmt) Float(i int) float64 { return float64(C.sqlite3_column_double(s.st, C.int(i))) }

// Text reads result column i as text.
func (s *Stmt) Text(i int) string {
	p := C.sqlite3_column_text(s.st, C.int(i))
	if p == nil {
		return ""
	}
	return C.GoStringN((*C.char)(unsafe.Pointer(p)), C.sqlite3_column_bytes(s.st, C.int(i)))
}

// Value reads result column i as nil, int64, float64 or string.
func (s *Stmt) Value(i int) any {
	switch s.Type(i) {
	case Integer:
		return s.Int64(i)
	case Float:
		return s.Float(i)
	case Text, Blob:
		return s.Text(i)
	}
	return nil
}
