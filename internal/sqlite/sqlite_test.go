//go:build cgo

package sqlite

import (
	"math"
	"testing"
)

func TestPrepareSingle(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Exec("CREATE TABLE t (a INTEGER)"); err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{"SELECT 1", "SELECT 1;", "SELECT 1; -- done", "SELECT 1 /* a */ ;  \n", "SELECT ';' AS x"} {
		st, err := db.PrepareSingle(ok)
		if err != nil {
			t.Errorf("%q: %v", ok, err)
			continue
		}
		st.Finalize()
	}
	for _, bad := range []string{"SELECT 1; SELECT 2", "SELECT 1; DELETE FROM t", "", "  -- nothing"} {
		if st, err := db.PrepareSingle(bad); err == nil {
			st.Finalize()
			t.Errorf("%q was accepted", bad)
		}
	}
	// Interrupt with nothing running changes nothing.
	db.Interrupt()
	st, err := db.PrepareSingle("SELECT count(*) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Finalize()
	if ok, err := st.Step(); !ok || err != nil {
		t.Fatalf("step after an idle interrupt: %v %v", ok, err)
	}
}

func TestBasics(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v := Version(); v != "3.53.4" {
		t.Errorf("version %s", v)
	}
	if err := db.Exec("CREATE TABLE t (a INTEGER, b REAL, c TEXT)"); err != nil {
		t.Fatal(err)
	}
	st, err := db.Prepare("INSERT INTO t (a, b, c) VALUES (?, ?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	r := &Rows{Types: []byte("ift")}
	for i := 0; i < 3; i++ {
		r.Ints = append(r.Ints, int64(i))
		r.Reals = append(r.Reals, float64(i)+0.5)
		r.Offs = append(r.Offs, int32(len(r.Text)))
		s := []string{"alpha", "beta", "gamma"}[i]
		r.Text = append(r.Text, s...)
		r.Lens = append(r.Lens, int32(len(s)))
		r.N++
	}
	if err := st.RunRows(r); err != nil {
		t.Fatal(err)
	}
	q, err := db.Prepare("SELECT a, b, c, ln(b) FROM t ORDER BY a")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		ok, err := q.Step()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, q.Text(2))
		if q.Float(3) != Log(q.Float(1)) {
			t.Errorf("ln(%v) = %v in SQL, %v from C", q.Float(1), q.Float(3), Log(q.Float(1)))
		}
	}
	q.Reset()
	if len(got) != 3 || got[2] != "gamma" {
		t.Errorf("rows %v", got)
	}
	if math.Abs(Log(math.E)-1) > 1e-15 {
		t.Errorf("Log(e) = %v", Log(math.E))
	}
}
