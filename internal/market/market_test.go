package market

import (
	"math"
	"os"
	"testing"
)

// The day as tools/market-hash.mjs reports it from the JavaScript copy.
const (
	wantTrades = 4048210
	wantHash   = 1206980826
)

func TestSameDayAsJavaScript(t *testing.T) {
	var h uint32 = 2166136261
	mix := func(x uint32) { h = (h ^ x) * 16777619 }
	m := New()
	n := 0
	for s := 0; s < Day; s++ {
		o := m.Next()
		for i := range o.Sym {
			b := math.Float64bits(o.Price[i])
			mix(uint32(o.TS))
			mix(uint32(o.Sym[i]))
			mix(uint32(b))
			mix(uint32(b >> 32))
			mix(uint32(o.Size[i]))
		}
		n += len(o.Sym)
	}
	if n != wantTrades || h != wantHash {
		t.Errorf("the day has %d trades with hash %d; the JavaScript copy makes %d with hash %d", n, h, wantTrades, wantHash)
	}
}

func TestPolicyIsTheExample(t *testing.T) {
	b, err := os.ReadFile("../../examples/trades.precompute")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != Policy {
		t.Error("internal/market/policy.go differs from examples/trades.precompute; copy the example over")
	}
}
