package compile

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// TestGolden compiles every example policy and compares it with the stored output,
// so any change to the generated SQL shows up in review.
func TestGolden(t *testing.T) {
	files, _ := filepath.Glob("../examples/*.precompute")
	if len(files) == 0 {
		t.Fatal("no example policies found")
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out, err := Source(string(src), filepath.Base(f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		name := strings.TrimSuffix(filepath.Base(f), ".precompute")
		for suffix, text := range map[string]string{".sql": out.Schema, ".distill.sql": out.Distill} {
			golden := filepath.Join("testdata", name+suffix)
			if *update {
				if err := os.WriteFile(golden, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./compile -update)", err)
			}
			if string(want) != text {
				t.Errorf("%s differs from %s; run go test ./compile -update and review the diff", f, golden)
			}
		}
	}
}

func TestDeterministic(t *testing.T) {
	src, _ := os.ReadFile("../examples/latency.precompute")
	a, _ := Source(string(src), "x")
	b, _ := Source(string(src), "x")
	if a.Schema != b.Schema || a.Distill != b.Distill {
		t.Error("compiling twice gives different SQL")
	}
}

func TestNameClash(t *testing.T) {
	_, err := Source("stream s { value v } precompute s_raw = count(s)", "x")
	if err == nil || !strings.Contains(err.Error(), "already used by stream s") {
		t.Errorf("got %v", err)
	}
	_, err = Source("stream s { key v_sum text value v rollup 1m keep 1d }", "x")
	if err == nil || !strings.Contains(err.Error(), "clashes with a column") {
		t.Errorf("got %v", err)
	}
}

func TestNoKeysStream(t *testing.T) {
	out, err := Source("stream s { value v rollup 1m keep 1d quantiles v samples 2 per 1m anomalies v z > 3 } precompute p = p50(s.v) precompute a = avg(s.v)", "x")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"g INTEGER PRIMARY KEY CHECK (g = 1)", "ON CONFLICT (g) DO UPDATE", "ON CONFLICT (b) DO UPDATE", "sum(n) OVER ()"} {
		if !strings.Contains(out.Schema, want) {
			t.Errorf("schema lacks %q", want)
		}
	}
	if strings.Contains(out.Schema, "latency_win.g") || strings.Contains(out.Schema, "s_win.g") {
		t.Error("window lookups must not refer to a g column")
	}
}
