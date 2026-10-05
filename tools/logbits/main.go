// Command logbits writes test values and Go's math.Log of each, as little-endian float64 pairs,
// for tools/logbits.mjs to compare with the ln() of SQLite's WebAssembly build.
//
//	go run ./tools/logbits | node tools/logbits.mjs
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"math"
	"math/rand"
	"os"
)

func main() {
	n := flag.Int("n", 2000000, "how many values")
	flag.Parse()
	r := rand.New(rand.NewSource(42))
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	buf := make([]byte, 16)
	for i := 0; i < *n; i++ {
		var x float64
		switch i % 4 {
		case 0: // latencies in milliseconds, two decimals
			x = math.Round((0.5+r.Float64()*5000)*100) / 100
		case 1: // prices, two decimals
			x = math.Round((1+r.Float64()*500)*100) / 100
		case 2: // spread over many orders of magnitude
			x = math.Exp(r.Float64()*60 - 30)
		default: // token counts
			x = float64(1 + r.Intn(200000))
		}
		binary.LittleEndian.PutUint64(buf[0:], math.Float64bits(x))
		binary.LittleEndian.PutUint64(buf[8:], math.Float64bits(math.Log(x)))
		w.Write(buf)
	}
}
