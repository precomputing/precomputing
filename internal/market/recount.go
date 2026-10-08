package market

// Candle is one window of one symbol, counted straight from the trades.
type Candle struct {
	W, N                   int64
	Open, High, Low, Close float64
	Volume, Turnover, Sum  float64
}

// Recount keeps every candle and the quote board, counted from the trades as they are made, with
// the same arithmetic in the same order the policy asks for. It never sees the Engine or the file,
// so comparing the two checks the Engine end to end. It is the Go copy of Expected in run.js.
type Recount struct {
	Res    []int64
	Win    []map[int64]*Candle // per resolution, keyed by w*8+symbol
	Quote  []*Candle           // per symbol, the whole day
	Trades int64
}

// NewRecount starts an empty recount for 1-second, 1-minute and 1-hour candles.
func NewRecount() *Recount {
	r := &Recount{Res: []int64{1, 60, 3600}, Quote: make([]*Candle, len(Symbols))}
	for range r.Res {
		r.Win = append(r.Win, map[int64]*Candle{})
	}
	return r
}

func add(c **Candle, w int64, p, z float64) {
	nv := float64(p * z) // rounded here, never fused with the add below
	if *c == nil {
		*c = &Candle{W: w, N: 1, Open: p, High: p, Low: p, Close: p, Volume: z, Turnover: nv, Sum: p}
		return
	}
	x := *c
	x.N++
	if !(x.Low < p) {
		x.Low = p
	}
	if x.High < p {
		x.High = p
	}
	x.Close = p
	x.Volume = x.Volume + z
	x.Turnover = x.Turnover + nv
	x.Sum = x.Sum + p
}

// Add counts a second of trades.
func (r *Recount) Add(o *Second) {
	for i, s := range o.Sym {
		p, z := o.Price[i], o.Size[i]
		for k, res := range r.Res {
			w := o.TS - o.TS%res
			key := w*8 + int64(s)
			c := r.Win[k][key]
			add(&c, w, p, z)
			r.Win[k][key] = c
		}
		add(&r.Quote[s], 0, p, z)
	}
	r.Trades += int64(len(o.Sym))
}
