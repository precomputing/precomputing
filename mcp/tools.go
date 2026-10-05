package mcp

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// The five tools. Every one only reads, and every one answers with text: a line or two saying
// what the rows are, then the rows as CSV.

type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
	run         func(r Reader, a args) (string, error)
}

var readOnly = map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	timeSchema = func(what string) map[string]any {
		return map[string]any{"type": []string{"string", "integer"}, "description": what +
			" An RFC 3339 time such as 2026-09-28T10:00:00Z or 2026-09-28T10:30:00-04:00, or Unix seconds. A time without an offset is UTC."}
	}
	whereSchema = map[string]any{
		"type":        "object",
		"description": `Keep only rows whose columns have these values, such as {"customer": "harbor"}. A list matches any of its values: {"endpoint": ["/api/cart", "/api/checkout"]}.`,
		"additionalProperties": map[string]any{"anyOf": []any{
			map[string]any{"type": []string{"string", "number"}},
			map[string]any{"type": "array", "items": map[string]any{"type": []string{"string", "number"}}},
		}},
	}
	limitSchema = func(def, max int) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "maximum": max, "default": def, "description": fmt.Sprintf("Most rows to return. Default %d, at most %d.", def, max)}
	}
)

var tools = []*tool{
	{
		Name:  "describe_file",
		Title: "What this file keeps ready",
		Description: "Describes the Precomputing file this server reads: its streams of events with their keys, " +
			"the values each key takes and the time they cover; the answers kept ready (precomputes and quotas); " +
			"the windows kept at each resolution; what is kept whole (raw events, samples, unusual events, log templates); " +
			"and the file's other tables and views. Call it first: the other tools take the names it gives. Times are UTC.",
		InputSchema: obj(map[string]any{}),
		run:         describeFile,
	},
	{
		Name:  "get_answer",
		Title: "Read an answer kept ready",
		Description: "Reads a precompute, a quota, a count of refused events or another view of the file, such as invoices, " +
			"filtered by its columns. A precompute is kept current as events arrive, so this reads a few rows and computes nothing.",
		InputSchema: obj(map[string]any{
			"name":   map[string]any{"type": "string", "description": "An answer named by describe_file."},
			"where":  whereSchema,
			"period": map[string]any{"type": "string", "description": "For answers kept per hour, day or month: the period, such as 2026-09, 2026-09-28 or 2026-09-28T14. A month also matches its days and hours."},
			"limit":  limitSchema(50, 500),
		}, "name"),
		run: getAnswer,
	},
	{
		Name:  "get_windows",
		Title: "Summaries of a stream over a time range",
		Description: "Summarizes a stream's events in a time range, from the windows the file keeps: count, average, minimum, maximum, " +
			"first, last and sum of each value, percentiles where the file keeps sketches, and how many events the anomaly rule flagged. " +
			"Use every to choose the rows: one per minute (1m), per 15 minutes (15m), per hour (1h), and so on, or all for one summary of the whole range. " +
			"Percentiles are within the sketch's stated accuracy, usually 1%.",
		InputSchema: obj(map[string]any{
			"stream": map[string]any{"type": "string", "description": "A stream named by describe_file."},
			"from":   timeSchema("Start of the range. Default: an hour before the newest event."),
			"to":     timeSchema("End of the range, not included. Default: just after the newest event."),
			"where":  whereSchema,
			"by":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Keys to keep apart. Default: every key of the stream. [] adds all keys together."},
			"every":  map[string]any{"type": "string", "description": "Length of each row's window, such as 10s, 1m, 5m, 1h or 1d, a multiple of a window the file keeps; or all for one summary of the range. Default: the finest kept window that gives at most 60 rows per key."},
			"stats":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Figures to return: n, avg, min, max, first, last, sum, std, and percentiles such as p50, p90, p95, p99 or p999 where the file keeps a sketch. Default: n, avg, min, max, first, last and sum, with p50, p95 and p99 when there is a sketch."},
			"limit":  limitSchema(100, 1000),
		}, "stream"),
		run: getWindows,
	},
	{
		Name:  "get_kept",
		Title: "Events kept whole",
		Description: "Fetches events the file keeps whole: raw events for as long as the policy keeps them, samples kept from every window, " +
			"unusual events kept with their z-score, or the log templates the file has learned with when each was first seen. " +
			"For a question such as what is new since an incident began, ask for templates from that time.",
		InputSchema: obj(map[string]any{
			"kind":     map[string]any{"type": "string", "enum": []string{"raw", "samples", "anomalies", "templates"}, "description": "raw, samples, anomalies (unusual events) or templates (log line patterns)."},
			"stream":   map[string]any{"type": "string", "description": "A stream named by describe_file. Not needed for templates."},
			"from":     timeSchema("Keep events at or after this time; for templates, first seen at or after it."),
			"to":       timeSchema("Keep events before this time."),
			"where":    whereSchema,
			"contains": map[string]any{"type": "string", "description": "Only log lines, or templates, that contain this text, ignoring case."},
			"order":    map[string]any{"type": "string", "enum": []string{"newest", "oldest", "unusual"}, "description": "Newest first (the default), oldest first, or for anomalies the most unusual first."},
			"limit":    limitSchema(20, 200),
		}, "kind"),
		run: getKept,
	},
	{
		Name:  "query",
		Title: "Read the file with SQL",
		Description: "Runs one read-only SQLite statement on the file and returns at most limit rows. Use it when the other tools do not fit; " +
			"describe_file names the tables. Numbers are shown to ten significant digits.",
		InputSchema: obj(map[string]any{
			"sql":   map[string]any{"type": "string", "description": "One SELECT, WITH, PRAGMA or EXPLAIN statement."},
			"limit": limitSchema(100, 1000),
		}, "sql"),
		run: runQuery,
	},
}

func init() {
	for _, t := range tools {
		t.Annotations = readOnly
	}
}

func findTool(name string) *tool {
	for _, t := range tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// maxText caps what one call returns, so a careless call cannot flood the model's context.
const maxText = 60000

// callTool runs a tool. A mistake in the arguments or a failed read is a tool error, which the
// model can read and correct.
func callTool(r Reader, t *tool, arguments map[string]any) (string, bool) {
	a := args{m: arguments, tool: t.Name}
	if err := a.only(t.InputSchema); err != nil {
		return err.Error(), true
	}
	text, err := t.run(r, a)
	if err != nil {
		return err.Error(), true
	}
	if len(text) > maxText {
		cut := strings.LastIndexByte(text[:maxText], '\n')
		if cut < 0 {
			cut = maxText
		}
		text = text[:cut+1] + fmt.Sprintf("(cut at %d characters; ask for fewer rows)\n", cut+1)
	}
	return text, false
}

// args are the arguments of one call.
type args struct {
	m    map[string]any
	tool string
}

func (a args) only(schema map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	var bad []string
	for k := range a.m {
		if _, ok := props[k]; !ok {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	var names []string
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("%s takes no arguments", a.tool)
	}
	return fmt.Errorf("%s does not take %s; it takes %s", a.tool, strings.Join(bad, ", "), strings.Join(names, ", "))
}

func (a args) has(name string) bool {
	v, ok := a.m[name]
	return ok && v != nil
}

func (a args) str(name string, required bool) (string, error) {
	v, ok := a.m[name]
	if !ok || v == nil {
		if required {
			return "", fmt.Errorf("%s needs %s", a.tool, name)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: %s must be text", a.tool, name)
	}
	return strings.TrimSpace(s), nil
}

func (a args) integer(name string, def, min, max int) (int, error) {
	v, ok := a.m[name]
	if !ok || v == nil {
		return def, nil
	}
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) {
		return 0, fmt.Errorf("%s: %s must be a whole number", a.tool, name)
	}
	if f < float64(min) || f > float64(max) {
		return 0, fmt.Errorf("%s: %s must be from %d to %d", a.tool, name, min, max)
	}
	return int(f), nil
}

func (a args) when(name string) (int64, bool, error) {
	v, ok := a.m[name]
	if !ok || v == nil {
		return 0, false, nil
	}
	t, err := parseTime(v)
	if err != nil {
		return 0, false, fmt.Errorf("%s: %s: %v", a.tool, name, err)
	}
	return t, true, nil
}

func (a args) list(name string) ([]string, bool, error) {
	v, ok := a.m[name]
	if !ok || v == nil {
		return nil, false, nil
	}
	xs, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok { // a single name given as text
			return []string{strings.TrimSpace(s)}, true, nil
		}
		return nil, false, fmt.Errorf("%s: %s must be a list of names", a.tool, name)
	}
	out := []string{}
	for _, x := range xs {
		s, ok := x.(string)
		if !ok {
			return nil, false, fmt.Errorf("%s: %s must be a list of names", a.tool, name)
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out, true, nil
}

// filter is one column of a where argument, with the values it may take.
type filter struct {
	col  string
	vals []any
}

// where reads a where argument against the columns it may name. Integer columns take whole
// numbers, given as numbers or as text; other columns compare as text.
func (a args) where(cols []string, intCol map[string]bool) ([]filter, error) {
	v, ok := a.m["where"]
	if !ok || v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`%s: where is an object such as {"customer": "harbor"}`, a.tool)
	}
	var names []string
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []filter
	for _, k := range names {
		found := false
		for _, c := range cols {
			if c == k {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("%s: where names %s, which is not a column here; the columns are %s", a.tool, k, strings.Join(cols, ", "))
		}
		raw := m[k]
		xs, isList := raw.([]any)
		if !isList {
			xs = []any{raw}
		}
		if len(xs) == 0 {
			return nil, fmt.Errorf("%s: where %s is an empty list", a.tool, k)
		}
		f := filter{col: k}
		for _, x := range xs {
			switch y := x.(type) {
			case string:
				if intCol[k] {
					var n int64
					if _, err := fmt.Sscan(strings.TrimSpace(y), &n); err != nil {
						return nil, fmt.Errorf("%s: %s is a whole number, not %q", a.tool, k, y)
					}
					f.vals = append(f.vals, n)
				} else {
					f.vals = append(f.vals, y)
				}
			case float64:
				if intCol[k] {
					if y != math.Trunc(y) {
						return nil, fmt.Errorf("%s: %s is a whole number", a.tool, k)
					}
					f.vals = append(f.vals, int64(y))
				} else {
					f.vals = append(f.vals, num(y))
				}
			case bool:
				n := int64(0)
				if y {
					n = 1
				}
				f.vals = append(f.vals, n)
			default:
				return nil, fmt.Errorf("%s: where %s takes text or numbers", a.tool, k)
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// sqlFilters writes filters as SQL conditions on a table alias (or none).
func sqlFilters(fs []filter, prefix string) (string, []any) {
	var conds []string
	var args []any
	for _, f := range fs {
		col := prefix + quoteName(f.col)
		if len(f.vals) == 1 {
			conds = append(conds, col+" = ?")
		} else {
			conds = append(conds, col+" IN ("+strings.TrimSuffix(strings.Repeat("?, ", len(f.vals)), ", ")+")")
		}
		args = append(args, f.vals...)
	}
	return strings.Join(conds, " AND "), args
}

// filterText says what filters were applied, for the line above the rows.
func filterText(fs []filter) string {
	var parts []string
	for _, f := range fs {
		var vs []string
		for _, v := range f.vals {
			vs = append(vs, cell(v))
		}
		if len(vs) == 1 {
			parts = append(parts, f.col+" = "+vs[0])
		} else {
			parts = append(parts, f.col+" in "+strings.Join(vs, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

// fixed reports the columns a filter pins to one value; they need no column in the rows.
func fixed(fs []filter) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		if len(f.vals) == 1 {
			out[f.col] = true
		}
	}
	return out
}
