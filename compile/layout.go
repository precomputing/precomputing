package compile

import (
	"math"
	"strconv"
	"strings"

	"precomputing.com/precomputing/policy"
)

// Layout names every table and column of a policy's file, with the constants its SQL uses.
// The Engine writes the same file from it, so both runtimes agree by construction.
type Layout struct {
	Policy  *policy.Policy
	Output  *Output
	Streams []*StreamLayout
	// Distill is the distill SQL split into statements, each taking :now.
	Distill []string
}

// StreamLayout is the part of the file that belongs to one stream.
type StreamLayout struct {
	Stream  *policy.Stream
	Numbers []string // values then derived values: the order of the window columns
	Raw     string   // raw events; empty when the stream keeps none
	Win     string   // window summaries; empty when there are no rollups
	Sample  string
	Base    string // anomaly baseline
	Anomaly string
	IDs     string // exact streams: identifiers seen, to refuse repeats
	Clock   string // exact streams: the time of the newest event
	Refused string // exact streams: events refused, by reason and key
	Line    bool   // a stream from logs: raw events, samples and anomalies keep the line
	// Sketches are the quantile sketch tables, one per sketched number, in stream order.
	Sketches []SketchLayout
	// Groups are the precompute states that read this stream, in the order the trigger updates them.
	Groups []*GroupLayout

	// The constants below are exactly the doubles the SQL literals stand for.
	LnGamma float64 // sketch bucket = ceil(ln(x) / LnGamma)
	Gamma   float64
	Alpha   float64 // baseline weight, 1 / memory
	Z2      float64 // z squared
	Clip    float64 // baseline moves at most Clip standard deviations per event
	Huber   float64 // variance correction for the clip
}

// SketchLayout is one quantile sketch table.
type SketchLayout struct {
	Field string
	Table string
	Res   []int64 // the rollups that sketch this number
}

// GroupLayout is the state behind one or more precomputes.
type GroupLayout struct {
	Stream *policy.Stream
	Table  string
	By     []string
	Per    string // "", "hour", "day" or "month"
	Quant  bool   // a quantile sketch instead of sums
	Field  string // for quantile groups
	// Columns kept for the precomputes, each a list of numbers in stream order.
	Sum, Min, Max, First, Last []string
	FirstTS, LastTS            bool
	// NoKey is true when the group has neither by nor per and so is one row with g = 1.
	NoKey   bool
	LnGamma float64
}

// Literal returns the double SQLite reads from the literal the compiler writes for x.
func Literal(x float64) float64 {
	v, _ := strconv.ParseFloat(num(x), 64)
	return v
}

// PeriodFormat is the strftime format of a per period, and the Go layout that prints the same text.
func PeriodFormat(per string) (sqlite, golang string) {
	switch per {
	case "hour":
		return "%Y-%m-%dT%H", "2006-01-02T15"
	case "day":
		return "%Y-%m-%d", "2006-01-02"
	case "month":
		return "%Y-%m", "2006-01"
	}
	return "", ""
}

// NewLayout compiles a policy and describes the file it makes.
func NewLayout(pol *policy.Policy, name string) (*Layout, error) {
	out, err := Compile(pol, name)
	if err != nil {
		return nil, err
	}
	c := &compiler{pol: pol, objects: map[string]string{}}
	l := &Layout{Policy: pol, Output: out, Distill: splitStatements(out.Distill)}
	groups := c.groups()
	for _, s := range pol.Streams {
		sl := &StreamLayout{
			Stream:  s,
			Numbers: s.Numbers(),
			Gamma:   Literal(gamma(s.Accuracy)),
			LnGamma: Literal(math.Log(gamma(s.Accuracy))),
			Clip:    Literal(Clip),
			Huber:   Literal(huberK(Clip)),
		}
		if s.RawKeep != 0 || s.RawUntilClosed {
			sl.Raw = raw(s)
		}
		if len(s.Rollups) > 0 {
			sl.Win = win(s)
		}
		if s.Samples != nil {
			sl.Sample = sample(s)
		}
		sl.Line = s.FromLogs
		if s.Exact {
			sl.Clock, sl.Refused = clock(s), refused(s)
			if s.ID != nil {
				sl.IDs = ids(s)
			}
		}
		if a := s.Anomalies; a != nil {
			sl.Base, sl.Anomaly = base(s), anomaly(s)
			sl.Alpha = Literal(1 / float64(a.Memory))
			sl.Z2 = Literal(a.Z * a.Z)
		}
		for _, f := range c.sketched(s) {
			sk := SketchLayout{Field: f, Table: sk(s, f)}
			for _, r := range s.Rollups {
				for _, q := range r.Quantiles {
					if q == f {
						sk.Res = append(sk.Res, r.Res)
					}
				}
			}
			sl.Sketches = append(sl.Sketches, sk)
		}
		for _, g := range groups {
			if g.stream != s {
				continue
			}
			gl := &GroupLayout{
				Stream: s, Table: g.table, By: g.by, Per: g.per, Quant: g.quant, Field: g.field,
				Sum: g.has(g.needSum), Min: g.has(g.needMin), Max: g.has(g.needMax),
				First: g.has(g.needFst), Last: g.has(g.needLst),
				FirstTS: len(g.needFst) > 0, LastTS: len(g.needLst) > 0,
				NoKey:   len(g.keyCols()) == 0 && !g.quant,
				LnGamma: sl.LnGamma,
			}
			sl.Groups = append(sl.Groups, gl)
		}
		l.Streams = append(l.Streams, sl)
	}
	return l, nil
}

// splitStatements splits compiled SQL into statements, dropping comment lines.
func splitStatements(sql string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(t, ";") {
			out = append(out, strings.TrimSuffix(strings.TrimSpace(cur.String()), ";"))
			cur.Reset()
		}
	}
	return out
}
