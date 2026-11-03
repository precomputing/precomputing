package migrate

import (
	"fmt"
	"slices"
	"strings"

	"precomputing.com/precomputing/policy"
)

// compare checks the new policy against the old one at the level of meaning: the schema check
// catches changed tables, and this catches changes that keep the tables but change what their
// rows mean, such as a different sketch accuracy or another period for an exact stream. It fills
// Added and Changed, and appends a reason for every change it refuses.
func (p *Plan) compare(why *[]string) {
	refuse := func(format string, args ...any) { *why = append(*why, fmt.Sprintf(format, args...)) }
	added := func(format string, args ...any) { p.Added = append(p.Added, fmt.Sprintf(format, args...)) }
	changed := func(format string, args ...any) { p.Changed = append(p.Changed, fmt.Sprintf(format, args...)) }
	o, n := p.old, p.new

	if o.Logs != nil {
		if n.Logs == nil {
			refuse("the logs block would go; streams from logs and the templates learned need it")
		} else {
			if o.Logs.Similarity != n.Logs.Similarity || o.Logs.Depth != n.Logs.Depth {
				refuse("the templates' similarity and depth stay as they were: the templates learned so far were built with them")
			}
			if o.Logs.Format != n.Logs.Format {
				changed("log lines are read with the new format")
			}
			if !slices.Equal(o.Logs.Masks, n.Logs.Masks) {
				changed("log messages are masked with the new masks before their templates are learned")
			}
		}
	} else if n.Logs != nil {
		added("the logs block")
	}

	for _, os := range o.Streams {
		ns := n.Stream(os.Name)
		if ns == nil {
			refuse("stream %s would go; a stream stays once a file has it", os.Name)
			continue
		}
		compareStream(os, ns, refuse, added, changed)
	}
	for _, ns := range n.Streams {
		if o.Stream(ns.Name) == nil {
			added("stream %s", ns.Name)
		}
	}

	oldPC := map[string]*policy.Precompute{}
	for _, pc := range o.Precomputes {
		oldPC[pc.Name] = pc
	}
	for _, pc := range o.Precomputes {
		var np *policy.Precompute
		for _, x := range n.Precomputes {
			if x.Name == pc.Name {
				np = x
			}
		}
		switch {
		case np == nil:
			refuse("precompute %s would go", pc.Name)
		case precomputeText(np) != precomputeText(pc):
			refuse("precompute %s would change from %s to %s; what it holds was counted the old way", pc.Name, precomputeText(pc), precomputeText(np))
		}
	}
	for _, pc := range n.Precomputes {
		if oldPC[pc.Name] != nil {
			continue
		}
		// Precomputes of one stream with the same by and per share their state. A new one that reads
		// state an old one already fills answers from everything that state holds, from the start.
		var shares []string
		for _, x := range o.Precomputes {
			if StateTable(x) == StateTable(pc) {
				shares = append(shares, x.Name)
			}
		}
		if len(shares) > 0 {
			changed("precompute %s = %s answers from the start of the file: it reads the state %s already keeps", pc.Name, precomputeText(pc), strings.Join(shares, ", "))
		} else {
			added("precompute %s = %s, counting from the migration", pc.Name, precomputeText(pc))
		}
	}
	for _, q := range o.Quotas {
		found := false
		for _, x := range n.Quotas {
			if x.Name == q.Name {
				found = true
				if x.Precompute != q.Precompute {
					refuse("quota %s would change its precompute; its limits were set for %s", q.Name, q.Precompute)
				}
			}
		}
		if !found {
			refuse("quota %s would go, and with it the limits set for it", q.Name)
		}
	}
	for _, q := range n.Quotas {
		if !slices.ContainsFunc(o.Quotas, func(x *policy.Quota) bool { return x.Name == q.Name }) {
			added("quota %s on %s", q.Name, q.Precompute)
		}
	}
}

func compareStream(o, n *policy.Stream, refuse, added, changed func(string, ...any)) {
	name := o.Name
	fields := func(fs []*policy.Field) string {
		var parts []string
		for _, f := range fs {
			parts = append(parts, f.Name+" "+f.Type)
		}
		return strings.Join(parts, ", ")
	}
	if fields(o.Keys) != fields(n.Keys) {
		refuse("stream %s's keys would change from (%s) to (%s); its rows are keyed the old way", name, fields(o.Keys), fields(n.Keys))
	}
	if fields(o.Values) != fields(n.Values) {
		refuse("stream %s's values would change from (%s) to (%s)", name, fields(o.Values), fields(n.Values))
	}
	derived := func(ds []*policy.Derived) string {
		var parts []string
		for _, d := range ds {
			parts = append(parts, d.Name+" = "+exprText(d.Expr))
		}
		return strings.Join(parts, "; ")
	}
	if derived(o.Derived) != derived(n.Derived) {
		refuse("stream %s's derived values would change; the windows hold them as they were computed", name)
	}
	if o.Accuracy != n.Accuracy {
		refuse("stream %s's quantile accuracy stays %g%%: its sketches' buckets are that wide", name, o.Accuracy*100)
	}
	if o.FromLogs != n.FromLogs || (o.FromMetrics == "") != (n.FromMetrics == "") {
		refuse("stream %s would take its events from somewhere else", name)
	} else {
		if o.FromMetrics != n.FromMetrics {
			changed("stream %s takes metric %q from now on", name, n.FromMetrics)
		}
		if condText(o.Where) != condText(n.Where) {
			changed("stream %s takes the events where %s from now on", name, condText(n.Where))
		}
	}

	if o.Exact != n.Exact {
		refuse("stream %s would change whether it is exact", name)
	} else if o.Exact {
		switch {
		case (o.ID == nil) != (n.ID == nil) || (o.ID != nil && (o.ID.Name != n.ID.Name || o.ID.Type != n.ID.Type)):
			refuse("stream %s's id would change; the identifiers kept to refuse repeats are the old ones", name)
		case o.Period != n.Period:
			refuse("stream %s's period stays %s: its totals are counted per %s", name, o.Period, o.Period)
		case o.Close != n.Close:
			refuse("stream %s's close stays %s: a period it has closed must stay closed", name, policy.FormatDuration(o.Close))
		case o.Late != n.Late:
			refuse("stream %s's late stays as it was: what it refused as late must stay refused", name)
		}
		if o.Repeats != n.Repeats && o.ID != nil && n.ID != nil {
			changed("stream %s remembers identifiers for %s", name, policy.FormatDuration(n.Repeats))
		}
		if o.Ahead != n.Ahead {
			if n.Ahead == 0 {
				changed("stream %s no longer refuses events stamped ahead of the clock", name)
			} else {
				changed("stream %s refuses events more than %s ahead of the clock", name, policy.FormatDuration(n.Ahead))
			}
		}
		if o.RawAfterClose != n.RawAfterClose {
			changed("stream %s keeps raw events until closed + %s", name, policy.FormatDuration(n.RawAfterClose))
		}
	}

	if o.RawKeep != n.RawKeep {
		switch {
		case o.RawKeep == 0:
			added("raw events of stream %s", name)
		case n.RawKeep == 0:
			refuse("stream %s would stop keeping raw events, and the ones it holds would go", name)
		default:
			changed("stream %s keeps raw events for %s", name, keepText(n.RawKeep))
		}
	}
	for _, or := range o.Rollups {
		nr := n.Rollup(or.Res)
		if nr == nil {
			refuse("stream %s's rollup %s would go, with its windows", name, policy.FormatDuration(or.Res))
			continue
		}
		if !slices.Equal(or.Quantiles, nr.Quantiles) {
			refuse("stream %s's rollup %s keeps the quantiles it had (%s): a sketch added now would cover only part of its windows; add a new rollup with them instead",
				name, policy.FormatDuration(or.Res), strings.Join(or.Quantiles, ", "))
		}
		if or.Keep != nr.Keep {
			changed("stream %s keeps its %s windows for %s", name, policy.FormatDuration(or.Res), keepText(nr.Keep))
		}
	}
	for _, nr := range n.Rollups {
		if o.Rollup(nr.Res) == nil {
			added("stream %s's rollup %s, from the migration on", name, policy.FormatDuration(nr.Res))
		}
	}
	switch {
	case o.Samples == nil && n.Samples != nil:
		added("samples of stream %s", name)
	case o.Samples != nil && n.Samples == nil:
		refuse("stream %s would stop keeping samples, and the ones it holds would go", name)
	case o.Samples != nil && (o.Samples.N != n.Samples.N || o.Samples.Per != n.Samples.Per):
		changed("stream %s keeps %d samples per %s", name, n.Samples.N, policy.FormatDuration(n.Samples.Per))
	}
	oa, na := o.Anomalies, n.Anomalies
	switch {
	case oa == nil && na != nil:
		added("anomalies of stream %s, with a baseline that starts learning at the migration", name)
	case oa != nil && na == nil:
		refuse("stream %s would stop keeping anomalies, and the ones it holds would go", name)
	case oa != nil && (oa.Field != na.Field || oa.Log != na.Log || oa.Change != na.Change):
		refuse("stream %s's anomalies would judge something else; its baselines were learned from %s", name, anomalyText(oa))
	case oa != nil && (oa.Z != na.Z || oa.Memory != na.Memory || oa.Warmup != na.Warmup || oa.Keep != na.Keep || oa.Per != na.Per):
		changed("stream %s judges anomalies with z > %g, memory %d, warmup %d, keep %d per %s", name, na.Z, na.Memory, na.Warmup, na.Keep, policy.FormatDuration(na.Per))
	}
}

// StateTable is the table that holds a precompute's state, which precomputes of one stream with the
// same by and per share (quantiles have one per value).
func StateTable(pc *policy.Precompute) string {
	name := "_pc_" + pc.Stream
	if pc.Func == "quantile" {
		name = "_pcq_" + pc.Stream + "_" + pc.Field
	}
	if len(pc.By) > 0 {
		name += "_by_" + strings.Join(pc.By, "_")
	}
	if pc.Per != "" {
		name += "_per_" + pc.Per
	}
	return name
}

func keepText(k int64) string {
	if k < 0 {
		return "forever"
	}
	return policy.FormatDuration(k)
}

func anomalyText(a *policy.Anomalies) string {
	s := a.Field
	if a.Log {
		s += " log"
	}
	if a.Change {
		s += " change"
	}
	return s
}

func condText(cs []policy.Cond) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s = %q", c.Field, c.Value))
	}
	if len(parts) == 0 {
		return "(all)"
	}
	return strings.Join(parts, " and ")
}

func precomputeText(pc *policy.Precompute) string {
	s := pc.FuncAs + "(" + pc.Stream
	if pc.Field != "" {
		s += "." + pc.Field
	}
	s += ")"
	if len(pc.By) > 0 {
		s += " by " + strings.Join(pc.By, ", ")
	}
	if pc.Per != "" {
		s += " per " + pc.Per
	}
	return s
}

func exprText(e policy.Expr) string {
	switch x := e.(type) {
	case policy.Num:
		return x.Text
	case policy.Ref:
		return x.Name
	case policy.Neg:
		return "-(" + exprText(x.X) + ")"
	case policy.Binary:
		return "(" + exprText(x.L) + " " + string(x.Op) + " " + exprText(x.R) + ")"
	}
	return "?"
}
