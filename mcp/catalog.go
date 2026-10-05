package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The catalog is what a file says about itself: the policy it was made from and, in
// _precomputing_objects, every stream, window table, sketch, sample table, anomaly table,
// precompute and quota, with the details a reader needs. It is read on every call, so the
// server holds no state between calls.

type rollup struct {
	res, keep int64
	quantiles []string
}

type stream struct {
	name      string
	keys      []string
	intKey    map[string]bool
	values    []string
	numbers   []string // values, then derived values: the order of the window columns
	derived   []string
	rawTable  string
	rawKeep   int64
	fromLogs  bool
	where     map[string]string
	exact     bool
	period    string
	id        string
	refused   string
	rawAfter  int64 // exact streams: raw events stay this long after their period closes
	win       string
	rollups   []rollup // finest first
	sketches  map[string]*sketch
	samples   string
	sampleN   int64
	samplePer int64
	anomalies string
	anomaly   map[string]any
}

type sketch struct {
	table    string
	value    string
	res      []int64
	gamma    float64
	accuracy float64
}

func (sk *sketch) has(res int64) bool {
	for _, r := range sk.res {
		if r == res {
			return true
		}
	}
	return false
}

type answer struct {
	name, kind, stream string
	function           string
	value              string
	by                 []string
	per                string
	precompute         string // quotas: the precompute they limit
	accuracy           float64
}

type other struct {
	name, kind string // kind: view or table
	columns    []string
}

type catalog struct {
	format    string
	compiler  string
	policy    string
	streams   []*stream
	byName    map[string]*stream
	answers   []*answer // precomputes, then quotas
	answerBy  map[string]*answer
	refusals  []string // refusal count tables of exact streams
	others    []*other
	templates string // the table of log templates, when the file reads logs
	logFields []string
}

func (c *catalog) stream(name string) (*stream, error) {
	if s := c.byName[name]; s != nil {
		return s, nil
	}
	var names []string
	for _, s := range c.streams {
		names = append(names, s.name)
	}
	return nil, fmt.Errorf("the file has no stream %q; its streams are %s", name, strings.Join(names, ", "))
}

func stringsOf(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func intOf(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return 0
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// loadCatalog reads what a file says about itself.
func loadCatalog(r Reader) (*catalog, error) {
	meta, err := r.Read("SELECT key, value FROM _precomputing", nil, 0)
	if err != nil {
		return nil, fmt.Errorf("this is not a Precomputing file (%v)", err)
	}
	c := &catalog{byName: map[string]*stream{}, answerBy: map[string]*answer{}}
	for _, row := range meta.Rows {
		v := cell(row[1])
		switch cell(row[0]) {
		case "format":
			c.format = v
		case "compiler":
			c.compiler = v
		case "policy":
			c.policy = v
		}
	}
	objs, err := r.Read("SELECT name, kind, stream, detail FROM _precomputing_objects", nil, 0)
	if err != nil {
		return nil, err
	}
	type obj struct {
		name, kind, stream string
		d                  map[string]any
	}
	var list []obj
	known := map[string]bool{}
	for _, row := range objs.Rows {
		o := obj{name: cell(row[0]), kind: cell(row[1]), stream: cell(row[2])}
		if err := json.Unmarshal([]byte(cell(row[3])), &o.d); err != nil {
			return nil, fmt.Errorf("the file describes %s in a way this reader does not know: %v", o.name, err)
		}
		list = append(list, o)
		known[o.name] = true
	}
	// Streams first, then what hangs off them.
	for _, o := range list {
		if o.kind != "stream" {
			continue
		}
		s := &stream{
			name: o.name, keys: stringsOf(o.d["keys"]), values: stringsOf(o.d["values"]),
			rawTable: stringOf(o.d["raw_table"]), rawKeep: intOf(o.d["raw_keep_seconds"]),
			intKey: map[string]bool{}, sketches: map[string]*sketch{},
		}
		if ds, ok := o.d["derived"].([]any); ok {
			for _, d := range ds {
				if m, ok := d.(map[string]any); ok {
					s.derived = append(s.derived, stringOf(m["name"]))
				}
			}
		}
		s.numbers = append(append([]string{}, s.values...), s.derived...)
		if stringOf(o.d["from"]) == "logs" {
			s.fromLogs = true
			s.where = map[string]string{}
			if w, ok := o.d["where"].(map[string]any); ok {
				for k, v := range w {
					s.where[k] = stringOf(v)
				}
			}
		}
		if o.d["exact"] == true {
			s.exact = true
			s.period = stringOf(o.d["period"])
			s.id = stringOf(o.d["id"])
			s.refused = stringOf(o.d["refused_table"])
			s.rawAfter = intOf(o.d["raw_after_close_seconds"])
			c.refusals = append(c.refusals, s.refused)
		}
		c.streams = append(c.streams, s)
		c.byName[s.name] = s
	}
	for _, o := range list {
		s := c.byName[o.stream]
		switch o.kind {
		case "windows":
			if s == nil {
				continue
			}
			s.win = o.name
			if rs, ok := o.d["rollups"].([]any); ok {
				for _, x := range rs {
					m, _ := x.(map[string]any)
					s.rollups = append(s.rollups, rollup{res: intOf(m["res_seconds"]), keep: intOf(m["keep_seconds"]), quantiles: stringsOf(m["quantiles"])})
				}
			}
			sort.Slice(s.rollups, func(i, j int) bool { return s.rollups[i].res < s.rollups[j].res })
		case "sketch":
			if s == nil {
				continue
			}
			sk := &sketch{table: o.name, value: stringOf(o.d["value"]), accuracy: 0.01}
			if g, ok := o.d["gamma"].(float64); ok {
				sk.gamma = g
			}
			if a, ok := o.d["accuracy"].(float64); ok {
				sk.accuracy = a
			}
			if rs, ok := o.d["res_seconds"].([]any); ok {
				for _, x := range rs {
					sk.res = append(sk.res, intOf(x))
				}
			}
			s.sketches[sk.value] = sk
		case "samples":
			if s == nil {
				continue
			}
			s.samples, s.sampleN, s.samplePer = o.name, intOf(o.d["n"]), intOf(o.d["per_seconds"])
		case "anomalies":
			if s == nil {
				continue
			}
			s.anomalies, s.anomaly = o.name, o.d
		case "precompute", "quota":
			a := &answer{name: o.name, kind: o.kind, stream: o.stream, function: stringOf(o.d["function"]),
				value: stringOf(o.d["value"]), by: stringsOf(o.d["by"]), per: stringOf(o.d["per"]),
				precompute: stringOf(o.d["precompute"])}
			if acc, ok := o.d["accuracy"].(float64); ok {
				a.accuracy = acc
			}
			c.answers = append(c.answers, a)
			c.answerBy[a.name] = a
		case "logs":
			c.templates = stringOf(o.d["templates_table"])
			c.logFields = stringsOf(o.d["fields"])
		}
	}
	sort.SliceStable(c.answers, func(i, j int) bool { return c.answers[i].kind == "precompute" && c.answers[j].kind != "precompute" })
	// Key types come from the raw table, which every stream has.
	for _, s := range c.streams {
		cols, err := r.Read("SELECT name, type FROM pragma_table_info(?)", []any{s.rawTable}, 0)
		if err != nil {
			return nil, err
		}
		for _, row := range cols.Rows {
			if strings.EqualFold(cell(row[1]), "INTEGER") {
				s.intKey[cell(row[0])] = true
			}
		}
	}
	// The file's other tables and views: what the product keeps beside the policy, such as a price
	// list and the invoices read from the precomputes.
	internal := func(name string) bool {
		if known[name] || strings.HasPrefix(name, "_") || strings.HasPrefix(name, "sqlite_") {
			return true
		}
		for _, s := range c.streams {
			if strings.HasPrefix(name, s.name+"_") && (name == s.rawTable || name == s.win || name == s.samples || name == s.anomalies ||
				name == s.name+"_base" || name == s.name+"_ids" || name == s.name+"_clock" || name == s.refused) {
				return true
			}
		}
		for _, a := range c.answers {
			if a.kind == "quota" && name == a.name+"_limit" {
				return true
			}
		}
		return false
	}
	tabs, err := r.Read("SELECT name, type FROM sqlite_master WHERE type IN ('table', 'view') ORDER BY type DESC, name", nil, 0)
	if err != nil {
		return nil, err
	}
	for _, row := range tabs.Rows {
		name, kind := cell(row[0]), cell(row[1])
		if internal(name) {
			continue
		}
		cols, err := r.Read("SELECT name FROM pragma_table_info(?)", []any{name}, 0)
		if err != nil {
			return nil, err
		}
		o := &other{name: name, kind: kind}
		for _, cr := range cols.Rows {
			o.columns = append(o.columns, cell(cr[0]))
		}
		c.others = append(c.others, o)
	}
	return c, nil
}

// columnsOf lists the columns of a table or view.
func columnsOf(r Reader, name string) ([]string, error) {
	cols, err := r.Read("SELECT name FROM pragma_table_info(?)", []any{name}, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, row := range cols.Rows {
		out = append(out, cell(row[0]))
	}
	return out, nil
}

// quoteName quotes a name for SQL. Names come from the file's own catalog.
func quoteName(n string) string { return `"` + strings.ReplaceAll(n, `"`, `""`) + `"` }

// describeAnswer says in one line what an answer is.
func (a *answer) describe() string {
	if a.kind == "quota" {
		return fmt.Sprintf("%s: %s against a limit%s%s; columns used, lim, remaining, reached (1 when used has reached lim)",
			a.name, a.precompute, byText(a.by), perText(a.per))
	}
	arg := a.stream
	if a.value != "" {
		arg += "." + a.value
	}
	return fmt.Sprintf("%s = %s(%s)%s%s", a.name, a.function, arg, byText(a.by), perText(a.per))
}

func byText(by []string) string {
	if len(by) == 0 {
		return ""
	}
	return " by " + strings.Join(by, ", ")
}

func perText(per string) string {
	if per == "" {
		return ""
	}
	return " per " + per
}
