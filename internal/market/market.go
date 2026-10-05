// Package market is the Demo 2 scenario: one simulated trading day for eight invented symbols.
// It is a line-by-line copy of demo/engine/app/market.js and produces the very same trades, so the
// native tools and the browser demo feed the Engine identical input. Products are wrapped in
// float64() so that no compiler fuses them into a multiply-add, which would round differently.
package market

import "math"

// Open is 2026-09-28 09:30:00 New York time (13:30 UTC); the day lasts Day seconds, to 16:00.
const (
	Open = 1790602200
	Day  = 23400
)

// Symbols are invented; the prices are made up.
var Symbols = []string{"SIM1", "SIM2", "SIM3", "SIM4", "SIM5", "SIM6", "SIM7", "SIM8"}

var (
	startCents = []float64{18740, 6425, 41280, 2310, 9860, 14530, 3175, 5290}
	rate       = []float64{40, 34, 27, 23, 19, 15, 11, 7}
	dailyVol   = []float64{0.022, 0.018, 0.025, 0.03, 0.016, 0.02, 0.028, 0.024}
	spread     = []int{2, 1, 3, 1, 1, 2, 1, 1}
)

// Jump is a scripted move of one symbol's price by a factor at a second of the day.
type Jump struct {
	Sym    int
	T      int
	Factor float64
}

// Jumps are the day's two price jumps.
var Jumps = []Jump{
	{Sym: 3, T: 5537, Factor: 1.08},  // SIM4 +8% at 11:02:17
	{Sym: 5, T: 18065, Factor: 0.94}, // SIM6 -6% at 14:31:05
}

// Halt stops one symbol from 12:00 to 12:10; it reopens 3.5% lower.
var Halt = struct {
	Sym, From, To int
	Factor        float64
}{7, 9000, 9600, 0.965}

const (
	sqrt3    = 1.7320508075688772
	moveSeed = 4000
)

// Rand is mulberry32, the generator the JavaScript copy uses.
type Rand struct{ a uint32 }

// NewRand seeds a generator.
func NewRand(seed uint32) *Rand { return &Rand{a: seed} }

// Next returns a number in [0, 1).
func (r *Rand) Next() float64 {
	r.a += 0x6D2B79F5
	a := r.a
	t := (a ^ a>>15) * (1 | a)
	t = (t + (t^t>>7)*(61|t)) ^ t
	return float64(t^t>>14) / 4294967296
}

func bell(r *Rand) float64 {
	a := r.Next()
	b := r.Next()
	c := r.Next()
	d := r.Next()
	return float64((a + b + c + d - 2) * sqrt3)
}

// Activity is how busy the market is at second t, relative to an average moment.
func Activity(t int) float64 {
	x := float64(2*t)/Day - 1
	a := 0.45 + float64(float64(1.1*x)*x) + float64(float64(float64(float64(float64(float64(0.9*x)*x)*x)*x)*x)*x)
	a = float64(a * ramp(t, 1800, 3000, 1.6))
	a = float64(a * ramp(t, 10800, 14400, 0.65))
	a = float64(a * ramp(t, 18065, 18900, 1.35))
	return a
}

func ramp(t, from, to int, f float64) float64 {
	const e = 120
	var w float64
	if t <= from-e || t >= to+e {
		return 1
	}
	switch {
	case t < from:
		w = float64(t-(from-e)) / e
	case t > to:
		w = float64(to+e-t) / e
	default:
		w = 1
	}
	return 1 + float64((f-1)*w)
}

func surge(s, t int) float64 {
	for _, j := range Jumps {
		if j.Sym == s && t >= j.T && t < j.T+300 {
			return 3
		}
	}
	if s == Halt.Sym && t >= Halt.To && t < Halt.To+180 {
		return 4
	}
	return 1
}

var sizes = []struct {
	p   float64
	lot float64
}{{0.2, 0}, {0.62, 100}, {0.76, 200}, {0.84, 300}, {0.9, 500}, {0.955, 1000}, {0.985, 2000}, {1, 5000}}

// Second is the trades of one simulated second, symbols interleaved.
type Second struct {
	TS    int64
	Sym   []uint8
	Price []float64
	Size  []float64
}

// Market makes the day's trades one second at a time.
type Market struct {
	T      int
	mid    []float64
	side   []int
	sigma  []float64
	rates  *Rand
	moves  []*Rand
	flow   []*Rand
	extra  []Jump
	counts []int
}

// New starts the day at the open.
func New() *Market {
	m := &Market{rates: NewRand(11)}
	for s := range Symbols {
		m.mid = append(m.mid, startCents[s]+0.5)
		m.side = append(m.side, 1)
		m.sigma = append(m.sigma, dailyVol[s]/math.Sqrt(float64(rate[s]*Day)))
		m.moves = append(m.moves, NewRand(uint32(moveSeed+s)))
		m.flow = append(m.flow, NewRand(uint32(200+s)))
	}
	m.counts = make([]int, len(Symbols))
	return m
}

// JumpNow moves a symbol's price by a factor from the next second on.
func (m *Market) JumpNow(sym int, factor float64) {
	m.extra = append(m.extra, Jump{Sym: sym, T: m.T, Factor: factor})
}

func (m *Market) halted(s, t int) bool { return s == Halt.Sym && t >= Halt.From && t < Halt.To }

// Next makes the trades of the next second.
func (m *Market) Next() *Second {
	t := m.T
	act := Activity(t)
	total := 0
	for s := range Symbols {
		u := m.rates.Next()
		v := m.rates.Next()
		if m.halted(s, t) {
			m.counts[s] = 0
			continue
		}
		lam := float64(float64(rate[s]*act) * surge(s, t))
		z := float64((u + v - 1) * 2.449489742783178)
		n := int(math.Floor(float64(lam+float64(z*math.Sqrt(lam))) + 0.5))
		if n < 0 {
			n = 0
		}
		m.counts[s] = n
		total += n
		for _, j := range append(append([]Jump{}, Jumps...), m.extra...) {
			if j.Sym == s && j.T == t {
				m.mid[s] = float64(m.mid[s] * j.Factor)
			}
		}
		if s == Halt.Sym && t == Halt.To {
			m.mid[s] = float64(m.mid[s] * Halt.Factor)
		}
	}
	out := &Second{TS: Open + int64(t), Sym: make([]uint8, total), Price: make([]float64, total), Size: make([]float64, total)}
	left := append([]int{}, m.counts...)
	k := 0
	for k < total {
		for s := range Symbols {
			if left[s] == 0 {
				continue
			}
			left[s]--
			r, f := m.moves[s], m.flow[s]
			m.mid[s] = float64(m.mid[s] * (1 + float64(m.sigma[s]*bell(r))))
			if f.Next() < 0.4 {
				m.side[s] = -m.side[s]
			}
			sp := spread[s]
			bid := math.Floor(m.mid[s] - float64(sp-1)/2)
			cents := bid
			if m.side[s] > 0 {
				cents = bid + float64(sp)
			}
			q := f.Next()
			size := 0.0
			for _, z := range sizes {
				if q < z.p {
					if z.lot == 0 {
						size = 1 + math.Floor(float64(f.Next()*99))
					} else {
						size = z.lot
					}
					break
				}
			}
			out.Sym[k] = uint8(s)
			out.Price[k] = cents / 100
			out.Size[k] = size
			k++
		}
	}
	m.T++
	return out
}
