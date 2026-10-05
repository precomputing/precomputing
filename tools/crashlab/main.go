//go:build cgo

// Command crashlab kills "precomputing put" without warning, many times, while it takes in
// Demo 2's trading day, and checks that no acknowledged trade is ever lost.
//
// A feeder sends the day as CSV lines with sequence numbers. The Engine acknowledges with
// "ok SEQ" after each checkpoint. At random moments the lab sends SIGKILL, starts the Engine
// again, reads the "ready SEQ" it reports from its file and resends from SEQ + 1. Two things
// must hold: the file never holds less than the last acknowledgement before a kill, and at the
// end the file is identical, bit for bit, to the file of an uninterrupted run and to the file the
// compiled triggers make from the same trades.
//
//	go run ./tools/crashlab -bin build/precomputing -kills 100
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"precomputing.com/precomputing/compile"
	"precomputing.com/precomputing/internal/filecmp"
	"precomputing.com/precomputing/internal/market"
	"precomputing.com/precomputing/internal/sqlite"
	"precomputing.com/precomputing/policy"
)

type kill struct {
	N             int   `json:"n"`
	AfterMs       int   `json:"after_ms"`
	LastAck       int64 `json:"last_ack"`          // the last "ok SEQ" read before the kill
	Sent          int64 `json:"sent"`              // the last sequence number written to the pipe
	MidCheckpoint bool  `json:"mid_checkpoint"`    // "writing" seen with no "ok" after it
	Recovered     int64 `json:"recovered"`         // "ready SEQ" from the next start
	LostAck       bool  `json:"lost_acknowledged"` // recovered < last ack: must never happen
	RestartMs     int   `json:"restart_ms"`
}

type report struct {
	Machine       string          `json:"machine"`
	Trades        int64           `json:"trades"`
	Kills         []kill          `json:"kills"`
	KillCount     int             `json:"kill_count"`
	MidCheckpoint int             `json:"mid_checkpoint_kills"`
	LostAck       int             `json:"acknowledged_trades_lost"`
	Resent        int64           `json:"trades_resent"`
	Seconds       float64         `json:"seconds"`
	SameAsSteady  bool            `json:"identical_to_uninterrupted_run"`
	SameAsSQL     bool            `json:"identical_to_triggers"`
	Rows          int             `json:"rows_compared"`
	Values        int             `json:"values_compared"`
	Tables        []filecmp.Table `json:"tables"`
}

// feed writes the day's trades from sequence number from+1 on as CSV lines.
func feed(w io.Writer, from int64, sent *int64, mu *sync.Mutex) error {
	bw := bufio.NewWriterSize(w, 1<<16)
	m := market.New()
	var seq int64
	var b []byte
	for t := 0; t < market.Day; t++ {
		o := m.Next()
		for i, s := range o.Sym {
			seq++
			if seq <= from {
				continue
			}
			b = b[:0]
			b = strconv.AppendInt(b, seq, 10)
			b = append(b, ',')
			b = strconv.AppendInt(b, o.TS, 10)
			b = append(b, ',')
			b = append(b, market.Symbols[s]...)
			b = append(b, ',')
			b = strconv.AppendFloat(b, o.Price[i], 'f', -1, 64)
			b = append(b, ',')
			b = strconv.AppendInt(b, int64(o.Size[i]), 10)
			b = append(b, '\n')
			if _, err := bw.Write(b); err != nil {
				return err
			}
			if seq%512 == 0 {
				if err := bw.Flush(); err != nil {
					return err
				}
				mu.Lock()
				*sent = seq
				mu.Unlock()
			}
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	mu.Lock()
	*sent = seq
	mu.Unlock()
	return nil
}

func dayTrades() int64 {
	m := market.New()
	var n int64
	for t := 0; t < market.Day; t++ {
		n += int64(len(m.Next().Sym))
	}
	return n
}

// run starts the Engine on path and feeds it. With killAfter > 0 it is killed after that long,
// unless the day is done first. It returns what it saw.
func run(bin, pol, path string, killAfter time.Duration) (ready, lastAck, sent int64, mid, finished bool, err error) {
	cmd := exec.Command(bin, "put", "--policy", pol, "--seq", "--every", "50ms", "--trace", path)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return
	}
	var mu sync.Mutex
	readyc := make(chan int64, 1)
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 2 {
				continue
			}
			n, _ := strconv.ParseInt(f[1], 10, 64)
			mu.Lock()
			switch f[0] {
			case "ready":
				readyc <- n
			case "writing":
				mid = true
			case "ok":
				lastAck, mid = n, false
			}
			mu.Unlock()
		}
	}()
	select {
	case ready = <-readyc:
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		err = fmt.Errorf("no ready line")
		return
	}
	var fed int64 // written by the feeder under mu; copied out below
	feedDone := make(chan error, 1)
	go func() {
		e := feed(stdin, ready, &fed, &mu)
		stdin.Close()
		feedDone <- e
	}()
	var timer <-chan time.Time
	if killAfter > 0 {
		timer = time.After(killAfter)
	}
	select {
	case <-timer:
		mu.Lock()
		cmd.Process.Kill() // SIGKILL: no chance to write anything more
		m := mid
		mu.Unlock()
		cmd.Wait()
		<-outDone
		mu.Lock()
		defer mu.Unlock()
		return ready, lastAck, fed, m, false, nil
	case e := <-feedDone:
		if e != nil {
			err = e
			cmd.Process.Kill()
			cmd.Wait()
			return
		}
		err = cmd.Wait()
		<-outDone
		mu.Lock()
		defer mu.Unlock()
		return ready, lastAck, fed, false, true, err
	}
}

func triggers(pol *policy.Policy, path string) error {
	lay, err := compile.NewLayout(pol, "trades.precompute")
	if err != nil {
		return err
	}
	db, err := sqlite.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Exec(lay.Output.Schema); err != nil {
		return err
	}
	ins, err := db.Prepare("INSERT INTO trades (ts, symbol, price, size) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	distill := func(now int64) error {
		for _, q := range lay.Distill {
			st, err := db.Prepare(q)
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
	m := market.New()
	db.Exec("BEGIN")
	var now int64
	for t := 0; t < market.Day; t++ {
		o := m.Next()
		for i, s := range o.Sym {
			ins.Bind(1, o.TS)
			ins.Bind(2, market.Symbols[s])
			ins.Bind(3, o.Price[i])
			ins.Bind(4, int64(o.Size[i]))
			if _, err := ins.Step(); err != nil {
				return err
			}
			ins.Reset()
		}
		now = o.TS
		if t%60 == 59 {
			if err := distill(now); err != nil {
				return err
			}
		}
	}
	if err := distill(now); err != nil {
		return err
	}
	return db.Exec("COMMIT")
}

func main() {
	bin := flag.String("bin", "build/precomputing", "the precomputing binary")
	pol := flag.String("policy", "examples/trades.precompute", "the policy")
	kills := flag.Int("kills", 100, "how many times to kill the Engine")
	dir := flag.String("dir", "", "where to keep the files (default: a temporary directory)")
	out := flag.String("o", "build/crashlab.json", "the report")
	seed := flag.Int64("seed", 1, "seed for the kill times")
	flag.Parse()
	if *dir == "" {
		d, err := os.MkdirTemp("", "crashlab")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(d)
		*dir = d
	}
	src, err := os.ReadFile(*pol)
	if err != nil {
		panic(err)
	}
	p, err := policy.Parse(string(src))
	if err != nil {
		panic(err)
	}
	rep := &report{Trades: dayTrades()}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				rep.Machine = strings.TrimSpace(l[strings.Index(l, ":")+1:])
				break
			}
		}
	}
	fmt.Printf("The day: %d trades. Killing the Engine %d times while it takes them in.\n", rep.Trades, *kills)

	crashed := filepath.Join(*dir, "crashed.db")
	rnd := rand.New(rand.NewSource(*seed))
	start := time.Now()
	var prev *kill
	var holds int64
	for n := 1; ; n++ {
		after := time.Duration(0)
		if len(rep.Kills) < *kills {
			after = time.Duration(40+rnd.Intn(260)) * time.Millisecond
		}
		t0 := time.Now()
		ready, ack, sent, mid, finished, err := run(*bin, *pol, crashed, after)
		if err != nil {
			fmt.Println("run failed:", err)
			os.Exit(1)
		}
		if prev != nil {
			prev.Recovered = ready
			prev.LostAck = ready < prev.LastAck
			prev.RestartMs = int(time.Since(t0).Milliseconds())
			if prev.LostAck {
				rep.LostAck++
			}
			if sent := prev.Sent; sent > ready {
				rep.Resent += sent - ready
			}
		}
		if finished {
			break
		}
		k := kill{N: len(rep.Kills) + 1, AfterMs: int(after.Milliseconds()), LastAck: ack, Sent: sent, MidCheckpoint: mid}
		rep.Kills = append(rep.Kills, k)
		prev = &rep.Kills[len(rep.Kills)-1]
		if k.MidCheckpoint {
			rep.MidCheckpoint++
		}
		if held := max(ack, ready); held > holds {
			holds = held
		}
		if k.N%10 == 0 {
			fmt.Printf("  %3d kills; the file holds %d of %d trades\n", k.N, holds, rep.Trades)
		}
	}
	rep.KillCount = len(rep.Kills)
	rep.Seconds = time.Since(start).Seconds()
	fmt.Printf("%d kills in %.0f s (%d in the middle of a checkpoint); acknowledged trades lost: %d; trades resent: %d\n",
		rep.KillCount, rep.Seconds, rep.MidCheckpoint, rep.LostAck, rep.Resent)

	steady := filepath.Join(*dir, "steady.db")
	if _, _, _, _, _, err := run(*bin, *pol, steady, 0); err != nil {
		fmt.Println("uninterrupted run failed:", err)
		os.Exit(1)
	}
	sqlPath := filepath.Join(*dir, "triggers.db")
	if err := triggers(p, sqlPath); err != nil {
		fmt.Println("triggers failed:", err)
		os.Exit(1)
	}
	a, err := sqlite.OpenReadOnly(crashed)
	if err != nil {
		panic(err)
	}
	b, err := sqlite.OpenReadOnly(steady)
	if err != nil {
		panic(err)
	}
	c, err := sqlite.OpenReadOnly(sqlPath)
	if err != nil {
		panic(err)
	}
	t1, err := filecmp.Compare(b, a)
	if err != nil {
		panic(err)
	}
	t2, err := filecmp.Compare(c, a)
	if err != nil {
		panic(err)
	}
	rep.SameAsSteady, rep.SameAsSQL, rep.Tables = filecmp.Identical(t1), filecmp.Identical(t2), t2
	for _, t := range t2 {
		rep.Rows += t.Rows
		rep.Values += t.Values
	}
	fmt.Printf("The file after the kills against an uninterrupted run: %s. Against the compiled triggers: %s (%d rows, %d values).\n",
		map[bool]string{true: "identical", false: "DIFFERENT"}[rep.SameAsSteady],
		map[bool]string{true: "identical", false: "DIFFERENT"}[rep.SameAsSQL], rep.Rows, rep.Values)
	for _, t := range t2 {
		if t.Diffs > 0 {
			fmt.Printf("  %s: %s\n", t.Name, t.First)
		}
	}
	if j, err := json.MarshalIndent(rep, "", "  "); err == nil {
		os.MkdirAll(filepath.Dir(*out), 0o755)
		os.WriteFile(*out, j, 0o644)
	}
	if rep.LostAck > 0 || !rep.SameAsSteady || !rep.SameAsSQL {
		os.Exit(1)
	}
}
