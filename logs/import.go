package logs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"precomputing.com/precomputing/policy"
)

// Dashboard is the part of a dashboard definition the importer reads: the log format and panels
// that count, add up or take percentiles of log fields, filtered and grouped by other fields.
type Dashboard struct {
	Title string `json:"title"`
	Logs  struct {
		Format string   `json:"format"`
		Masks  []string `json:"masks"`
	} `json:"logs"`
	Panels []Panel `json:"panels"`
}

// Panel is one chart or table of a dashboard.
type Panel struct {
	Title   string            `json:"title"`
	Type    string            `json:"type"`    // "timeseries" (the default) or "toplist"
	Where   map[string]string `json:"where"`   // fields and the text they must equal
	Measure string            `json:"measure"` // count, rate, sum, avg, min, max or a percentile such as p95
	Field   string            `json:"field"`   // the field measured; none for count and rate
	By      []string          `json:"by"`      // fields the panel groups by
	Top     int               `json:"top"`     // for a toplist: how many rows
}

// Plan says which stream keeps a panel's answers ready.
type Plan struct {
	Panel
	Stream string `json:"stream"`
}

// Imported is a policy made from a dashboard, with the stream behind each panel.
type Imported struct {
	Policy string `json:"policy"`
	Plans  []Plan `json:"plans"`
}

var (
	percentileRE = regexp.MustCompile(`^p(\d{1,2}|999)$`)
	fieldRE      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
)

// DefaultFormat is the line format when a dashboard does not give one.
const DefaultFormat = "<timestamp> <level> <service> <message>"

// Import makes a policy that keeps every panel of a dashboard ready. Panels with the same where,
// by and field share one stream from logs with a 1-minute rollup, quantile sketches for
// percentiles, and unusual values kept whole. Two streams are always added: every line kept on site for 48 hours
// and counted per template, and error lines kept whole, up to five a minute of each kind.
func Import(data []byte) (*Imported, error) {
	var d Dashboard
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("the dashboard does not read: %v", err)
	}
	if len(d.Panels) == 0 {
		return nil, fmt.Errorf("the dashboard has no panels")
	}
	format := d.Logs.Format
	if format == "" {
		format = DefaultFormat
	}
	_, fields, err := policy.LogFormat(format)
	if err != nil {
		return nil, fmt.Errorf("logs.format: %v", err)
	}
	has := map[string]bool{}
	for _, f := range fields {
		has[f] = true
	}

	type group struct {
		name   string
		where  []policy.Cond
		by     []string
		values []string
		quant  map[string]bool
		panels []string
	}
	var groups []*group
	bySig := map[string]*group{}
	taken := map[string]bool{"lines": true, "errors": true}
	var plans []Plan
	for i, p := range d.Panels {
		name := p.Title
		if name == "" {
			name = fmt.Sprintf("panel %d", i+1)
		}
		switch p.Type {
		case "", "timeseries", "toplist":
		default:
			return nil, fmt.Errorf("%q: type is timeseries or toplist, not %q", name, p.Type)
		}
		m := strings.ToLower(p.Measure)
		switch {
		case m == "count" || m == "rate":
			if p.Field != "" {
				return nil, fmt.Errorf("%q: %s counts lines; it takes no field", name, m)
			}
		case m == "sum" || m == "avg" || m == "min" || m == "max" || percentileRE.MatchString(m):
			if p.Field == "" {
				return nil, fmt.Errorf("%q: %s needs a field", name, m)
			}
		default:
			return nil, fmt.Errorf("%q: measure is count, rate, sum, avg, min, max or a percentile such as p95, not %q", name, p.Measure)
		}
		p.Measure = m
		for _, f := range append(append([]string{p.Field}, p.By...), keys(p.Where)...) {
			if f != "" && !fieldRE.MatchString(f) {
				return nil, fmt.Errorf("%q: field %q: use lowercase letters, digits and _", name, f)
			}
		}
		for _, b := range p.By {
			if b == p.Field {
				return nil, fmt.Errorf("%q: %q is both measured and grouped by", name, b)
			}
		}
		var where []policy.Cond
		for _, k := range keys(p.Where) {
			where = append(where, policy.Cond{Field: k, Value: p.Where[k]})
		}
		// Panels share a stream when they filter, group and measure alike. A stream takes only
		// lines that have all its fields, so a count never shares with a measured field: lines
		// without that field would go uncounted.
		sig := fmt.Sprint(where, "|", p.By, "|", p.Field)
		g := bySig[sig]
		if g == nil {
			g = &group{where: where, by: p.By, quant: map[string]bool{}}
			g.name = streamName(where, p.Field, p.By, taken)
			bySig[sig] = g
			groups = append(groups, g)
		}
		if p.Field != "" && !contains(g.values, p.Field) {
			g.values = append(g.values, p.Field)
		}
		if percentileRE.MatchString(m) {
			g.quant[p.Field] = true
		}
		desc := m
		if p.Field != "" {
			desc += " of " + p.Field
		}
		if p.Type == "toplist" {
			if p.Top == 0 {
				p.Top = 10
			}
			desc = fmt.Sprintf("top %d by %s", p.Top, desc)
		}
		g.panels = append(g.panels, fmt.Sprintf("%q: %s", name, desc))
		plans = append(plans, Plan{Panel: p, Stream: g.name})
	}

	var b strings.Builder
	title := d.Title
	if title == "" {
		title = "a dashboard"
	} else {
		title = fmt.Sprintf("the dashboard %q", title)
	}
	fmt.Fprintf(&b, "# Made by precomputing import from %s.\n# Each stream from logs keeps some of its panels ready; edit freely.\n\n", title)
	fmt.Fprintf(&b, "logs {\n  format %s\n", quote(format))
	for _, m := range d.Logs.Masks {
		fmt.Fprintf(&b, "  mask   %s\n", quote(m))
	}
	b.WriteString("}\n\n")
	b.WriteString("# Every line: kept whole on site for 48 hours, counted per template, and one example of\n# each template every ten minutes to send upstream.\nstream lines from logs {\n")
	if has["service"] {
		b.WriteString("  key    service text\n")
	}
	if has["level"] {
		b.WriteString("  key    level text\n")
	}
	b.WriteString("  key    template integer\n  raw    keep 48h\n  rollup 1m keep 30d\n  rollup 10m keep 30d\n  samples 1 per 10m\n}\n")
	if has["level"] {
		b.WriteString("\n# Errors: whole lines, up to five a minute of each kind, to send upstream.\nstream errors from logs where level = \"ERROR\" {\n")
		if has["service"] {
			b.WriteString("  key    service text\n")
		}
		b.WriteString("  key    template integer\n  rollup 1m keep 30d\n  samples 5 per 1m\n}\n")
	}
	for _, g := range groups {
		b.WriteString("\n")
		for _, p := range g.panels {
			fmt.Fprintf(&b, "# %s\n", p)
		}
		fmt.Fprintf(&b, "stream %s from logs", g.name)
		for i, c := range g.where {
			if i == 0 {
				b.WriteString(" where ")
			} else {
				b.WriteString(" and ")
			}
			fmt.Fprintf(&b, "%s = %s", c.Field, quote(c.Value))
		}
		b.WriteString(" {\n")
		for _, k := range g.by {
			if k == "template" {
				b.WriteString("  key    template integer\n")
			} else {
				fmt.Fprintf(&b, "  key    %s text\n", k)
			}
		}
		for _, v := range g.values {
			fmt.Fprintf(&b, "  value  %s real\n", v)
		}
		b.WriteString("  rollup 1m keep 30d")
		var qs []string
		for _, v := range g.values {
			if g.quant[v] {
				qs = append(qs, v)
			}
		}
		if len(qs) > 0 {
			fmt.Fprintf(&b, " quantiles %s", strings.Join(qs, ", "))
		}
		b.WriteString("\n")
		if len(qs) > 0 {
			fmt.Fprintf(&b, "  anomalies %s log z > 4 keep 5 per 1m\n", qs[0])
		}
		b.WriteString("}\n")
	}
	text := b.String()
	if _, err := policy.Parse(text); err != nil {
		return nil, fmt.Errorf("the dashboard makes a policy that does not check: %v", err)
	}
	return &Imported{Policy: text, Plans: plans}, nil
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	// A fixed order, so that the same dashboard always makes the same policy.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

var nonName = regexp.MustCompile(`[^a-z0-9]+`)

// streamName names a panel stream from its where, field and by: web_ms_by_route, error_by_service.
func streamName(where []policy.Cond, field string, by []string, taken map[string]bool) string {
	var parts []string
	for _, c := range where {
		parts = append(parts, c.Value)
	}
	if len(parts) == 0 {
		parts = append(parts, "all")
	}
	if field != "" {
		parts = append(parts, field)
	}
	name := strings.Trim(nonName.ReplaceAllString(strings.ToLower(strings.Join(parts, "_")), "_"), "_")
	if len(by) > 0 {
		name += "_by_" + strings.Join(by, "_")
	}
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		name = "s_" + name
	}
	if len(name) > 40 {
		name = strings.TrimRight(name[:40], "_")
	}
	base := name
	for n := 2; taken[name] || policyReserved(name); n++ {
		name = fmt.Sprintf("%s_%d", base, n)
	}
	taken[name] = true
	return name
}

func policyReserved(name string) bool {
	_, err := policy.Parse("stream " + name + " { value v }")
	return err != nil
}
