package policy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DefaultAccuracy is the relative accuracy of quantile sketches when a stream does not set one.
const DefaultAccuracy = 0.01

// FieldKind says what a name in a stream refers to.
func (s *Stream) FieldKind(name string) string {
	for _, f := range s.Keys {
		if f.Name == name {
			return "key"
		}
	}
	for _, f := range s.Values {
		if f.Name == name {
			return "value"
		}
	}
	for _, d := range s.Derived {
		if d.Name == name {
			return "derived"
		}
	}
	return ""
}

// Numbers lists the values and derived values of a stream, in order.
func (s *Stream) Numbers() []string {
	var out []string
	for _, f := range s.Values {
		out = append(out, f.Name)
	}
	for _, d := range s.Derived {
		out = append(out, d.Name)
	}
	return out
}

// Rollup returns the rollup with the given resolution, or nil.
func (s *Stream) Rollup(res int64) *Rollup {
	for _, r := range s.Rollups {
		if r.Res == res {
			return r
		}
	}
	return nil
}

// Stream returns the stream with the given name, or nil.
func (p *Policy) Stream(name string) *Stream {
	for _, s := range p.Streams {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// DefaultSimilarity and DefaultDepth are the template settings when a logs block does not set them.
const (
	DefaultSimilarity = 0.5
	DefaultDepth      = 4
)

// LogFormat turns a line format into a regular expression the way Drain's reference
// implementation does: each <field> matches as little as it can, the text between fields is a
// regular expression itself, and a run of spaces matches any run of spaces. It returns the fields
// in order.
func LogFormat(format string) (*regexp.Regexp, []string, error) {
	spaces := regexp.MustCompile(` +`)
	field := regexp.MustCompile(`<[^<>]+>`)
	var b strings.Builder
	var fields []string
	b.WriteString("^")
	last := 0
	for _, m := range field.FindAllStringIndex(format, -1) {
		b.WriteString(spaces.ReplaceAllString(format[last:m[0]], `\s+`))
		name := strings.Trim(format[m[0]:m[1]], "<>")
		b.WriteString("(?P<" + name + ">.*?)")
		fields = append(fields, name)
		last = m[1]
	}
	b.WriteString(spaces.ReplaceAllString(format[last:], `\s+`))
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return re, fields, err
}

func checkLogs(lg *Logs) error {
	if lg.Format == "" {
		return &Error{lg.Pos, `the logs block needs a format, such as format "<timestamp> <level> <service> <message>"`}
	}
	_, fields, err := LogFormat(lg.Format)
	if err != nil {
		return &Error{lg.FormatPos, fmt.Sprintf("the format does not make a regular expression: %v", err)}
	}
	has := map[string]bool{}
	for _, f := range fields {
		if !nameRE.MatchString(f) {
			return &Error{lg.FormatPos, fmt.Sprintf("<%s>: a field name uses lowercase letters, digits and _", f)}
		}
		if has[f] {
			return &Error{lg.FormatPos, fmt.Sprintf("<%s> appears twice in the format", f)}
		}
		has[f] = true
	}
	if !has["message"] {
		return &Error{lg.FormatPos, "the format needs a <message> field: the text templates are learned from"}
	}
	if !has["timestamp"] && !(has["date"] && has["time"]) {
		return &Error{lg.FormatPos, "the format needs the time of each line: a <timestamp> field, or <date> and <time>"}
	}
	for _, f := range []string{"template", "line"} {
		if has[f] {
			return &Error{lg.FormatPos, fmt.Sprintf("<%s> is a name the Engine gives to every line; call the field something else", f)}
		}
	}
	lg.Fields = fields
	for i, m := range lg.Masks {
		if _, err := regexp.Compile(m); err != nil {
			return &Error{lg.MaskPos[i], fmt.Sprintf("the mask is not a regular expression: %v", err)}
		}
	}
	if lg.Similarity == 0 {
		lg.Similarity = DefaultSimilarity
	}
	if lg.Depth == 0 {
		lg.Depth = DefaultDepth
	}
	return nil
}

func checkFromLogs(pol *Policy, s *Stream) error {
	if !s.FromLogs {
		return nil
	}
	if pol.Logs == nil {
		return &Error{s.FromPos, "a stream from logs needs a logs block that says how lines are read"}
	}
	if s.Exact {
		return &Error{s.ExactPos, "a stream from logs cannot be exact in version 0.1"}
	}
	for _, f := range append(append([]*Field{}, s.Keys...), s.Values...) {
		if f.Name == "line" {
			return &Error{f.Pos, "line is the log line itself, kept with raw events, samples and anomalies; it is not a key or a value"}
		}
		if f.Name == "template" && f.Type != "integer" {
			return &Error{f.Pos, "template is the number of the line's template: declare it as key template integer"}
		}
	}
	return nil
}

func check(pol *Policy) error {
	if pol.Logs != nil {
		if err := checkLogs(pol.Logs); err != nil {
			return err
		}
	}
	seen := map[string]Pos{}
	for _, s := range pol.Streams {
		if err := checkStream(s); err != nil {
			return err
		}
		if err := checkFromLogs(pol, s); err != nil {
			return err
		}
		if prev, dup := seen[s.Name]; dup {
			return &Error{s.Pos, fmt.Sprintf("stream %q is already declared at %s", s.Name, prev)}
		}
		seen[s.Name] = s.Pos
	}
	pcs := map[string]Pos{}
	for _, pc := range pol.Precomputes {
		if err := checkName(pc.Pos, pc.Name, "precompute name"); err != nil {
			return err
		}
		if prev, dup := pcs[pc.Name]; dup {
			return &Error{pc.Pos, fmt.Sprintf("precompute %q is already declared at %s", pc.Name, prev)}
		}
		if _, clash := seen[pc.Name]; clash {
			return &Error{pc.Pos, fmt.Sprintf("precompute %q has the same name as a stream", pc.Name)}
		}
		pcs[pc.Name] = pc.Pos
		s := pol.Stream(pc.Stream)
		if s == nil {
			return &Error{pc.Pos, fmt.Sprintf("precompute %q reads stream %q, which is not declared", pc.Name, pc.Stream)}
		}
		if pc.Field == "" && pc.Func != "count" {
			return &Error{pc.Pos, fmt.Sprintf("%s needs a value, as in %s(%s.value_name)", pc.FuncAs, pc.FuncAs, pc.Stream)}
		}
		if pc.Field != "" {
			k := s.FieldKind(pc.Field)
			if k == "" {
				return &Error{pc.Pos, fmt.Sprintf("stream %q has no value %q", s.Name, pc.Field)}
			}
			if k == "key" {
				return &Error{pc.Pos, fmt.Sprintf("%q is a key of stream %q; precomputes read values (use \"by %s\" to group by it)", pc.Field, s.Name, pc.Field)}
			}
		}
		if s.Exact && pc.Func == "quantile" {
			return &Error{pc.Pos, fmt.Sprintf("%s reads an exact stream; percentiles come from approximate sketches", pc.FuncAs)}
		}
		by := map[string]bool{}
		for _, k := range pc.By {
			if s.FieldKind(k) != "key" {
				return &Error{pc.Pos, fmt.Sprintf("\"by %s\": %q is not a key of stream %q", k, k, s.Name)}
			}
			if by[k] {
				return &Error{pc.Pos, fmt.Sprintf("\"by\" lists %q twice", k)}
			}
			by[k] = true
		}
	}
	for _, q := range pol.Quotas {
		if err := checkName(q.Pos, q.Name, "quota name"); err != nil {
			return err
		}
		if _, clash := pcs[q.Name]; clash {
			return &Error{q.Pos, fmt.Sprintf("quota %q has the same name as a precompute", q.Name)}
		}
		if _, clash := seen[q.Name]; clash {
			return &Error{q.Pos, fmt.Sprintf("quota %q has the same name as a stream", q.Name)}
		}
		pcs[q.Name] = q.Pos
		var pc *Precompute
		for _, x := range pol.Precomputes {
			if x.Name == q.Precompute {
				pc = x
			}
		}
		if pc == nil {
			return &Error{q.Pos, fmt.Sprintf("quota %q limits precompute %q, which is not declared", q.Name, q.Precompute)}
		}
		if pc.Func != "sum" && pc.Func != "count" {
			return &Error{q.Pos, fmt.Sprintf("a quota limits a sum or a count; %q is %s", pc.Name, pc.FuncAs)}
		}
		if pc.Per == "" {
			return &Error{q.Pos, fmt.Sprintf("a quota starts again every period: give %q a per, such as per month", pc.Name)}
		}
	}
	return nil
}

func checkStream(s *Stream) error {
	if err := checkName(s.Pos, s.Name, "stream name"); err != nil {
		return err
	}
	names := map[string]Pos{}
	add := func(pos Pos, name, what string) error {
		if err := checkName(pos, name, what); err != nil {
			return err
		}
		if prev, dup := names[name]; dup {
			return &Error{pos, fmt.Sprintf("%q is already declared at %s", name, prev)}
		}
		names[name] = pos
		return nil
	}
	if s.ID != nil {
		if err := add(s.ID.Pos, s.ID.Name, "id name"); err != nil {
			return err
		}
	}
	for _, f := range s.Keys {
		if err := add(f.Pos, f.Name, "key name"); err != nil {
			return err
		}
	}
	for _, f := range s.Values {
		if err := add(f.Pos, f.Name, "value name"); err != nil {
			return err
		}
	}
	if len(s.Values) == 0 && !s.FromLogs {
		// A stream from logs may only count lines.
		return &Error{s.Pos, fmt.Sprintf("stream %q needs at least one value, as in \"value ms real\"", s.Name)}
	}
	numbers := map[string]bool{}
	for _, f := range s.Values {
		numbers[f.Name] = true
	}
	for _, d := range s.Derived {
		if err := add(d.Pos, d.Name, "derived value name"); err != nil {
			return err
		}
		if err := checkExpr(d.Expr, s, numbers); err != nil {
			return err
		}
		numbers[d.Name] = true
	}
	resSeen := map[int64]Pos{}
	for _, r := range s.Rollups {
		if prev, dup := resSeen[r.Res]; dup {
			return &Error{r.Pos, fmt.Sprintf("rollup %s is already declared at %s", FormatDuration(r.Res), prev)}
		}
		resSeen[r.Res] = r.Pos
		if r.Keep >= 0 && r.Keep < r.Res {
			return &Error{r.Pos, fmt.Sprintf("rollup %s keeps its windows for %s, less than one window", FormatDuration(r.Res), FormatDuration(r.Keep))}
		}
		q := map[string]bool{}
		for _, f := range r.Quantiles {
			if !numbers[f] {
				return &Error{r.Pos, fmt.Sprintf("quantiles %q: stream %q has no value by that name", f, s.Name)}
			}
			if q[f] {
				return &Error{r.Pos, fmt.Sprintf("quantiles lists %q twice", f)}
			}
			q[f] = true
		}
	}
	// Coarse windows are built at the same moment as fine ones, so the order in the file does not matter,
	// but sorting keeps the compiled SQL stable.
	sort.SliceStable(s.Rollups, func(i, j int) bool { return s.Rollups[i].Res < s.Rollups[j].Res })
	if s.Samples != nil && s.Rollup(s.Samples.Per) == nil {
		return &Error{s.Samples.Pos, fmt.Sprintf("samples per %s needs a rollup %s in the same stream", FormatDuration(s.Samples.Per), FormatDuration(s.Samples.Per))}
	}
	if a := s.Anomalies; a != nil {
		if !numbers[a.Field] {
			return &Error{a.Pos, fmt.Sprintf("anomalies %q: stream %q has no value by that name", a.Field, s.Name)}
		}
		if len(s.Rollups) == 0 {
			return &Error{a.Pos, "anomalies are counted per window, so the stream needs at least one rollup"}
		}
		if a.Per == 0 {
			a.Per = s.Rollups[0].Res
			for _, r := range s.Rollups {
				if r.Res >= 60 {
					a.Per = r.Res
					break
				}
			}
		}
		if s.Rollup(a.Per) == nil {
			return &Error{a.Pos, fmt.Sprintf("anomalies keep ... per %s needs a rollup %s in the same stream", FormatDuration(a.Per), FormatDuration(a.Per))}
		}
	}
	if s.Accuracy == 0 {
		s.Accuracy = DefaultAccuracy
	}
	return checkExact(s)
}

// checkExact checks the lines that belong to exact streams.
func checkExact(s *Stream) error {
	if !s.Exact {
		switch {
		case s.ID != nil:
			return &Error{s.ID.Pos, fmt.Sprintf("id belongs in an exact stream: write \"stream %s exact {\"", s.Name)}
		case s.Late != 0 || s.Period != "":
			return &Error{s.Pos, fmt.Sprintf("late and period belong in an exact stream: write \"stream %s exact {\"", s.Name)}
		case s.RawUntilClosed:
			return &Error{s.RawPos, fmt.Sprintf("raw until closed belongs in an exact stream: write \"stream %s exact {\"", s.Name)}
		}
		return nil
	}
	if s.Period == "" {
		return &Error{s.ExactPos, fmt.Sprintf("exact stream %q needs a period, as in \"period month close 24h\"", s.Name)}
	}
	if !s.RawUntilClosed {
		return &Error{s.ExactPos, fmt.Sprintf("exact stream %q keeps every event until its period closes: write \"raw until closed + 90d\"", s.Name)}
	}
	for _, r := range s.Rollups {
		if len(r.Quantiles) > 0 {
			return &Error{r.Pos, "an exact stream keeps exact answers; quantile sketches are approximate"}
		}
	}
	return nil
}

func checkExpr(e Expr, s *Stream, numbers map[string]bool) error {
	switch x := e.(type) {
	case Ref:
		if !numbers[x.Name] {
			if s.FieldKind(x.Name) == "key" {
				return &Error{x.Pos, fmt.Sprintf("%q is a key; a derived value uses values", x.Name)}
			}
			return &Error{x.Pos, fmt.Sprintf("%q is not a value declared above this line", x.Name)}
		}
	case Binary:
		if err := checkExpr(x.L, s, numbers); err != nil {
			return err
		}
		return checkExpr(x.R, s, numbers)
	case Neg:
		return checkExpr(x.X, s, numbers)
	}
	return nil
}
