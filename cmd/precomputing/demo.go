package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/engine/sqlitestore"
	"precomputing.com/precomputing/internal/filecmp"
	"precomputing.com/precomputing/internal/market"
	"precomputing.com/precomputing/internal/sqlite"
	"precomputing.com/precomputing/internal/version"
	"precomputing.com/precomputing/policy"
)

// demoReport is what "precomputing demo --json" prints.
type demoReport struct {
	Version     string  `json:"version"`
	SQLite      string  `json:"sqlite"`
	Machine     string  `json:"machine"`
	CPUs        int     `json:"cpus"`
	Trades      int64   `json:"trades"`
	Seconds     float64 `json:"seconds"`
	PerSecond   float64 `json:"trades_per_second"`
	Checkpoints int64   `json:"checkpoints"`
	Every       int     `json:"checkpoint_every_trades"`
	Sync        string  `json:"sync"`
	FileBytes   int64   `json:"file_bytes"`
	Candles     int     `json:"candles_checked"`
	CandleDiffs int     `json:"candle_differences"`
	QuoteDiffs  int     `json:"quote_board_differences"`
	TotalOK     bool    `json:"trades_total_ok"`
	Race        *race   `json:"race,omitempty"`
}

type race struct {
	Trades        int             `json:"trades"`
	EngineSeconds float64         `json:"engine_seconds"`
	SQLSeconds    float64         `json:"sql_seconds"`
	EngineRate    float64         `json:"engine_trades_per_second"`
	SQLRate       float64         `json:"sql_trades_per_second"`
	Rows          int             `json:"rows_compared"`
	Values        int             `json:"values_compared"`
	Identical     bool            `json:"identical"`
	Tables        []filecmp.Table `json:"tables"`
}

func machine() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				if i := strings.Index(l, ":"); i >= 0 {
					return strings.TrimSpace(l[i+1:])
				}
			}
		}
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

func demoCmd(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	every := fs.Int("every-trades", 10000, "checkpoint after this many trades")
	raceN := fs.Int("race", 200000, "trades to race through the Engine and the triggers (0 to skip)")
	dbPath := fs.String("db", "", "keep the Engine's file here (default: a temporary file, removed afterwards)")
	sync := fs.String("sync", "full", "full, normal or off")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	say := func(format string, a ...any) {
		if !*asJSON {
			fmt.Printf(format+"\n", a...)
		}
	}
	path := *dbPath
	if path == "" {
		dir, err := os.MkdirTemp("", "precomputing-demo")
		if err != nil {
			return fail("%v", err)
		}
		defer os.RemoveAll(dir)
		path = filepath.Join(dir, "trades.db")
	} else {
		os.Remove(path)
		os.Remove(path + "-wal")
		os.Remove(path + "-shm")
	}
	pol, err := policy.Parse(market.Policy)
	if err != nil {
		return fail("%v", err)
	}
	st, err := sqlitestore.Open(path, sqlitestore.Options{Sync: *sync})
	if err != nil {
		return fail("%v", err)
	}
	eng, err := engine.Open(st, pol, "trades.precompute")
	if err != nil {
		return fail("%v", err)
	}
	rep := &demoReport{Version: version.Version, SQLite: sqlite.Version(), Machine: machine(), CPUs: runtime.NumCPU(), Every: *every, Sync: *sync}
	say("Precomputing %s, SQLite %s, %s (%d CPUs)", rep.Version, rep.SQLite, rep.Machine, rep.CPUs)
	say("A simulated trading day: eight invented symbols, 09:30 to 16:00 New York time.")
	si := eng.Stream("trades")
	keys := make([]int32, len(market.Symbols))
	for i, s := range market.Symbols {
		keys[i], _ = eng.Key(si, s)
	}
	m := market.New()
	rc := market.NewRecount()
	var ts []int64
	var ks []int32
	var vals []float64
	var seq, since int64
	start := time.Now()
	for t := 0; t < market.Day; t++ {
		o := m.Next()
		rc.Add(o)
		ts, ks, vals = ts[:0], ks[:0], vals[:0]
		for i, s := range o.Sym {
			ts = append(ts, o.TS)
			ks = append(ks, keys[s])
			vals = append(vals, o.Price[i], o.Size[i])
		}
		if _, err := eng.PutBatch(si, "demo", seq+1, ts, ks, vals); err != nil {
			return fail("%v", err)
		}
		seq += int64(len(o.Sym))
		since += int64(len(o.Sym))
		if since >= int64(*every) {
			if err := eng.Checkpoint(); err != nil {
				return fail("checkpoint: %v", err)
			}
			since = 0
		}
		if t%3600 == 3599 {
			say("  %s New York  %d trades", clock(t+1), seq)
		}
	}
	if err := eng.Checkpoint(); err != nil {
		return fail("checkpoint: %v", err)
	}
	el := time.Since(start)
	rep.Trades = seq
	rep.Seconds = el.Seconds()
	rep.PerSecond = float64(seq) / el.Seconds()
	rep.Checkpoints = eng.Stats().Checkpoints
	st.Close()
	if fi, err := os.Stat(path); err == nil {
		rep.FileBytes = fi.Size()
		if w, err := os.Stat(path + "-wal"); err == nil {
			rep.FileBytes += w.Size()
		}
	}
	say("\n%d trades in %.2f s: %.0f trades a second, with a checkpoint every %d trades (%d in all, sync %s).",
		rep.Trades, rep.Seconds, rep.PerSecond, *every, rep.Checkpoints, *sync)

	db, err := sqlite.OpenReadOnly(path)
	if err != nil {
		return fail("%v", err)
	}
	rep.Candles, rep.CandleDiffs, rep.QuoteDiffs, rep.TotalOK, err = checkRecount(db, rc, market.Open+market.Day-1)
	db.Close()
	if err != nil {
		return fail("%v", err)
	}
	say("Recount: %d candles checked against every trade, %d differences; quote board %d differences; trades_total %s.",
		rep.Candles, rep.CandleDiffs, rep.QuoteDiffs, map[bool]string{true: "right", false: "WRONG"}[rep.TotalOK])

	if *raceN > 0 {
		r, err := runRace(pol, *raceN, *every)
		if err != nil {
			return fail("race: %v", err)
		}
		rep.Race = r
		say("\nRace, the first %d trades: the Engine %.0f a second, the triggers %.0f a second (%.1fx).",
			r.Trades, r.EngineRate, r.SQLRate, r.SQLSeconds/r.EngineSeconds)
		say("The two files: %d rows, %d values, %s.", r.Rows, r.Values, map[bool]string{true: "identical", false: "DIFFERENT"}[r.Identical])
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rep)
	}
	if rep.CandleDiffs != 0 || rep.QuoteDiffs != 0 || !rep.TotalOK || (rep.Race != nil && !rep.Race.Identical) {
		return 1
	}
	return 0
}

func clock(t int) string {
	s := 34200 + t
	return fmt.Sprintf("%02d:%02d", s/3600, s/60%60)
}

// checkRecount compares the file's candles and quote board with the recount. 1-second candles are
// compared for the last 50 minutes, which the policy keeps.
func checkRecount(db *sqlite.DB, rc *market.Recount, now int64) (checked, diffs, quoteDiffs int, totalOK bool, err error) {
	for k, res := range rc.Res {
		lo := int64(0)
		if res == 1 {
			lo = now - 3000
		}
		r, err := query(db, `SELECT w, symbol, n, price_first, price_max, price_min, price_last, size_sum, notional_sum, price_sum
			FROM trades_win WHERE res = ? AND w >= ?`, res, lo)
		if err != nil {
			return 0, 0, 0, false, err
		}
		want := 0
		for _, c := range rc.Win[k] {
			if c.W >= lo {
				want++
			}
		}
		checked += want
		if len(r.Rows) != want {
			diffs += abs(len(r.Rows) - want)
		}
		sym := map[string]int64{}
		for i, s := range market.Symbols {
			sym[s] = int64(i)
		}
		for _, row := range r.Rows {
			w := row[0].(int64)
			c := rc.Win[k][w*8+sym[row[1].(string)]]
			if c == nil || c.N != row[2].(int64) || c.Open != row[3].(float64) || c.High != row[4].(float64) || c.Low != row[5].(float64) ||
				c.Close != row[6].(float64) || c.Volume != row[7].(float64) || c.Turnover != row[8].(float64) || c.Sum != row[9].(float64) {
				diffs++
			}
		}
	}
	q, err := query(db, `SELECT l.symbol, l.value, o.value, h.value, lo.value, v.value, t.value, n.value
		FROM last_price l JOIN day_open o USING (symbol) JOIN day_high h USING (symbol, period)
		JOIN day_low lo USING (symbol, period) JOIN day_volume v USING (symbol, period)
		JOIN day_turnover t USING (symbol, period) JOIN day_trades n USING (symbol, period)`)
	if err != nil {
		return 0, 0, 0, false, err
	}
	for _, row := range q.Rows {
		i := -1
		for j, s := range market.Symbols {
			if s == row[0] {
				i = j
			}
		}
		c := rc.Quote[i]
		if c.Close != row[1].(float64) || c.Open != row[2].(float64) || c.High != row[3].(float64) || c.Low != row[4].(float64) ||
			c.Volume != row[5].(float64) || c.Turnover != row[6].(float64) || c.N != row[7].(int64) {
			quoteDiffs++
		}
	}
	if len(q.Rows) != len(market.Symbols) {
		quoteDiffs++
	}
	tot, err := query(db, "SELECT value FROM trades_total")
	if err != nil {
		return 0, 0, 0, false, err
	}
	totalOK = len(tot.Rows) == 1 && tot.Rows[0][0].(int64) == rc.Trades
	return checked, diffs, quoteDiffs, totalOK, nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// runRace feeds the first n trades to a fresh Engine and to the compiled triggers, one after the
// other on the same thread, and compares the two files.
func runRace(pol *policy.Policy, n, every int) (*race, error) {
	var seconds []*market.Second
	m := market.New()
	total := 0
	for total < n {
		o := m.Next()
		seconds = append(seconds, o)
		total += len(o.Sym)
	}
	r := &race{Trades: total}

	est, err := sqlitestore.Open(":memory:", sqlitestore.Options{})
	if err != nil {
		return nil, err
	}
	defer est.Close()
	eng, err := engine.Open(est, pol, "trades.precompute")
	if err != nil {
		return nil, err
	}
	si := eng.Stream("trades")
	keys := make([]int32, len(market.Symbols))
	for i, s := range market.Symbols {
		keys[i], _ = eng.Key(si, s)
	}
	start := time.Now()
	var seq int64
	since := 0
	for _, o := range seconds {
		for i, s := range o.Sym {
			seq++
			if err := eng.Put(si, "race", seq, o.TS, keys[s], []float64{o.Price[i], o.Size[i]}); err != nil {
				return nil, err
			}
		}
		since += len(o.Sym)
		if since >= every {
			if err := eng.Checkpoint(); err != nil {
				return nil, err
			}
			since = 0
		}
	}
	if err := eng.Checkpoint(); err != nil {
		return nil, err
	}
	r.EngineSeconds = time.Since(start).Seconds()

	lay, err := compile.NewLayout(pol, "trades.precompute")
	if err != nil {
		return nil, err
	}
	sdb, err := sqlite.Open(":memory:")
	if err != nil {
		return nil, err
	}
	defer sdb.Close()
	if err := sdb.Exec(lay.Output.Schema); err != nil {
		return nil, err
	}
	ins, err := sdb.Prepare("INSERT INTO trades (ts, symbol, price, size) VALUES (?, ?, ?, ?)")
	if err != nil {
		return nil, err
	}
	distill := func(now int64) error {
		for _, q := range lay.Distill {
			st, err := sdb.Prepare(q)
			if err != nil {
				return err
			}
			st.Bind(st.ParamIndex(":now"), now)
			if _, err := st.Step(); err != nil {
				return err
			}
			st.Reset()
		}
		return nil
	}
	start = time.Now()
	sdb.Exec("BEGIN")
	since = 0
	var now int64
	for _, o := range seconds {
		for i, s := range o.Sym {
			ins.Bind(1, o.TS)
			ins.Bind(2, market.Symbols[s])
			ins.Bind(3, o.Price[i])
			ins.Bind(4, int64(o.Size[i]))
			if _, err := ins.Step(); err != nil {
				return nil, err
			}
			ins.Reset()
		}
		now = o.TS
		since += len(o.Sym)
		if since >= every {
			if err := distill(now); err != nil {
				return nil, err
			}
			sdb.Exec("COMMIT")
			sdb.Exec("BEGIN")
			since = 0
		}
	}
	if err := distill(now); err != nil {
		return nil, err
	}
	sdb.Exec("COMMIT")
	r.SQLSeconds = time.Since(start).Seconds()
	r.EngineRate = float64(total) / r.EngineSeconds
	r.SQLRate = float64(total) / r.SQLSeconds
	r.Tables, err = filecmp.Compare(sdb, est.DB())
	if err != nil {
		return nil, err
	}
	for _, t := range r.Tables {
		r.Rows += t.Rows
		r.Values += t.Values
	}
	r.Identical = filecmp.Identical(r.Tables)
	return r, nil
}
