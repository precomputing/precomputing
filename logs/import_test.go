package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"precomputing.com/precomputing/compile"
)

func TestImport(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "examples", "shop-dashboard.json"))
	if err != nil {
		t.Fatal(err)
	}
	imp, err := Import(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + imp.Policy)
	if _, err := compile.Source(imp.Policy, "shop.precompute"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Requests a minute by route":      "web_by_route",
		"p95 latency by route":            "web_ms_by_route",
		"Errors a minute by service":      "error_by_service",
		"Payments by provider and result": "payments_by_provider_result",
		"Revenue a minute":                "checkout_total",
		"Top searches":                    "search_by_q",
	}
	for _, p := range imp.Plans {
		if want[p.Title] != p.Stream {
			t.Errorf("%q is answered by %q, want %q", p.Title, p.Stream, want[p.Title])
		}
	}
	for _, s := range []string{"quantiles ms", "anomalies ms log z > 4", `where level = "ERROR"`, "samples 1 per 10m", "raw    keep 48h"} {
		if !strings.Contains(imp.Policy, s) {
			t.Errorf("the policy should contain %q", s)
		}
	}
	again, _ := Import(data)
	if again.Policy != imp.Policy {
		t.Error("the same dashboard should make the same policy")
	}
}

func TestImportErrors(t *testing.T) {
	for src, msg := range map[string]string{
		`{"panels": []}`: "no panels",
		`{"panels": [{"title": "a", "measure": "median", "field": "ms"}]}`:            "not \"median\"",
		`{"panels": [{"title": "a", "measure": "avg"}]}`:                              "needs a field",
		`{"panels": [{"title": "a", "measure": "count", "field": "ms"}]}`:             "takes no field",
		`{"panels": [{"title": "a", "measure": "count", "by": ["http.status"]}]}`:     "lowercase letters",
		`{"panels": [{"title": "a", "measure": "sum", "field": "x", "by": ["x"]}]}`:   "both measured",
		`{"panels": [{"title": "a", "measure": "count"}], "extra": 1}`:                "does not read",
		`{"logs": {"format": "<level> <message>"}, "panels": [{"measure": "count"}]}`: "time of each line",
	} {
		_, err := Import([]byte(src))
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: got %v, want ...%s...", src, err, msg)
		}
	}
}
