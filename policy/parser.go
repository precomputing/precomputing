package policy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Parse reads a policy and checks it. Every error carries the line and column.
func Parse(src string) (*Policy, error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	pol, err := p.policy()
	if err != nil {
		return nil, err
	}
	pol.Source = src
	if err := check(pol); err != nil {
		return nil, err
	}
	return pol, nil
}

type parser struct {
	toks []Token
	i    int
}

func (p *parser) peek() Token { return p.toks[p.i] }
func (p *parser) next() Token {
	t := p.toks[p.i]
	if t.Kind != EOF {
		p.i++
	}
	return t
}

func errAt(t Token, format string, args ...any) error {
	return &Error{t.Pos, fmt.Sprintf(format, args...)}
}

func describe(t Token) string {
	if t.Kind == EOF {
		return "the end of the file"
	}
	return fmt.Sprintf("%q", t.Text)
}

func (p *parser) expect(k Kind, what string) (Token, error) {
	t := p.next()
	if t.Kind != k {
		return t, errAt(t, "expected %s, found %s", what, describe(t))
	}
	return t, nil
}

func (p *parser) word(w string) error {
	t := p.next()
	if t.Kind != Ident || t.Text != w {
		return errAt(t, "expected %q, found %s", w, describe(t))
	}
	return nil
}

func (p *parser) isWord(w string) bool {
	t := p.peek()
	return t.Kind == Ident && t.Text == w
}

func (p *parser) name(what string) (Token, error) {
	t := p.next()
	if t.Kind != Ident {
		return t, errAt(t, "expected %s, found %s", what, describe(t))
	}
	return t, nil
}

func (p *parser) policy() (*Policy, error) {
	pol := &Policy{}
	for p.peek().Kind != EOF {
		t := p.peek()
		switch {
		case t.Kind == Ident && t.Text == "stream":
			s, err := p.stream()
			if err != nil {
				return nil, err
			}
			pol.Streams = append(pol.Streams, s)
		case t.Kind == Ident && t.Text == "precompute":
			pc, err := p.precompute()
			if err != nil {
				return nil, err
			}
			pol.Precomputes = append(pol.Precomputes, pc)
		case t.Kind == Ident && t.Text == "logs":
			if pol.Logs != nil {
				return nil, errAt(t, "logs is declared twice")
			}
			lg, err := p.logs()
			if err != nil {
				return nil, err
			}
			pol.Logs = lg
		case t.Kind == Ident && t.Text == "quota":
			p.next()
			nt, err := p.name("a name for the quota")
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(Assign, "'='"); err != nil {
				return nil, err
			}
			pt, err := p.name("the precompute the quota limits")
			if err != nil {
				return nil, err
			}
			pol.Quotas = append(pol.Quotas, &Quota{Name: nt.Text, Precompute: pt.Text, Pos: t.Pos})
		default:
			return nil, errAt(t, "expected \"stream\", \"precompute\", \"quota\" or \"logs\", found %s", describe(t))
		}
	}
	return pol, nil
}

// logs reads the block that says how log lines are read:
//
//	logs {
//	  format "<timestamp> <level> <service> <message>"
//	  mask   "[0-9a-f]{8}-[0-9a-f-]{27}"
//	  templates similarity 0.5 depth 4
//	}
func (p *parser) logs() (*Logs, error) {
	start := p.next() // logs
	lg := &Logs{Pos: start.Pos}
	if _, err := p.expect(LBrace, "'{' to open the logs block"); err != nil {
		return nil, err
	}
	for {
		t := p.next()
		if t.Kind == RBrace {
			return lg, nil
		}
		if t.Kind != Ident {
			return nil, errAt(t, "expected format, mask or templates, found %s", describe(t))
		}
		switch t.Text {
		case "format":
			if lg.Format != "" {
				return nil, errAt(t, "format is declared twice")
			}
			v, err := p.expect(String, "the line format in double quotes, such as \"<timestamp> <level> <service> <message>\"")
			if err != nil {
				return nil, err
			}
			lg.Format, lg.FormatPos = v.Text, v.Pos
		case "mask":
			v, err := p.expect(String, "a regular expression in double quotes")
			if err != nil {
				return nil, err
			}
			lg.Masks = append(lg.Masks, v.Text)
			lg.MaskPos = append(lg.MaskPos, v.Pos)
		case "templates":
			for p.isWord("similarity") || p.isWord("depth") {
				w := p.next()
				if w.Text == "similarity" {
					v := p.next()
					if (v.Kind != Number && v.Kind != Int) || v.Num <= 0 || v.Num > 1 {
						return nil, errAt(v, "similarity is a number above 0 and at most 1, such as 0.5, found %s", describe(v))
					}
					lg.Similarity = v.Num
				} else {
					n, v, err := p.integer("the depth of the template tree")
					if err != nil {
						return nil, err
					}
					if n < 3 || n > 10 {
						return nil, errAt(v, "the template tree's depth is from 3 to 10")
					}
					lg.Depth = n
				}
			}
		default:
			return nil, errAt(t, "expected format, mask or templates, found %s", describe(t))
		}
	}
}

// keep reads a duration or the word forever. Forever is -1.
func (p *parser) keep() (int64, error) {
	t := p.next()
	if t.Kind == Ident && t.Text == "forever" {
		return -1, nil
	}
	if t.Kind != Duration {
		return 0, errAt(t, "expected a duration such as 30d, or \"forever\", found %s", describe(t))
	}
	if t.Secs <= 0 {
		return 0, errAt(t, "a duration must be longer than zero")
	}
	return t.Secs, nil
}

func (p *parser) duration() (Token, error) {
	t := p.next()
	if t.Kind != Duration {
		return t, errAt(t, "expected a duration such as 1m, found %s", describe(t))
	}
	if t.Secs <= 0 {
		return t, errAt(t, "a duration must be longer than zero")
	}
	return t, nil
}

func (p *parser) integer(what string) (int, Token, error) {
	t := p.next()
	if t.Kind != Int {
		return 0, t, errAt(t, "expected %s (a whole number), found %s", what, describe(t))
	}
	return int(t.Num), t, nil
}

func (p *parser) stream() (*Stream, error) {
	start := p.next() // stream
	nt, err := p.name("a stream name")
	if err != nil {
		return nil, err
	}
	s := &Stream{Name: nt.Text, Pos: start.Pos}
	if p.isWord("exact") {
		et := p.next()
		s.Exact, s.ExactPos = true, et.Pos
	}
	if p.isWord("from") {
		ft := p.next()
		if err := p.word("logs"); err != nil {
			return nil, err
		}
		s.FromLogs, s.FromPos = true, ft.Pos
		if p.isWord("where") {
			p.next()
			for {
				f, err := p.name("a log field")
				if err != nil {
					return nil, err
				}
				if _, err := p.expect(Assign, "'='"); err != nil {
					return nil, err
				}
				v, err := p.expect(String, "the text the field must equal, in double quotes")
				if err != nil {
					return nil, err
				}
				s.Where = append(s.Where, Cond{Field: f.Text, Value: v.Text, Pos: f.Pos})
				if !p.isWord("and") {
					break
				}
				p.next()
			}
		}
	}
	if _, err := p.expect(LBrace, "'{' to open the stream"); err != nil {
		return nil, err
	}
	for {
		t := p.next()
		if t.Kind == RBrace {
			return s, nil
		}
		if t.Kind != Ident {
			return nil, errAt(t, "expected a stream line (key, value, derive, raw, rollup, samples, anomalies, quantile accuracy, or in an exact stream id, late and period), found %s", describe(t))
		}
		switch t.Text {
		case "key", "value":
			ft, err := p.name("a field name")
			if err != nil {
				return nil, err
			}
			f := &Field{Name: ft.Text, Pos: ft.Pos, Type: map[string]string{"key": "text", "value": "real"}[t.Text]}
			if nx := p.peek(); nx.Kind == Ident && (nx.Text == "text" || nx.Text == "integer" || nx.Text == "real") {
				p.next()
				if t.Text == "key" && nx.Text == "real" {
					return nil, errAt(nx, "a key is text or integer; real numbers belong in a value")
				}
				if t.Text == "value" && nx.Text == "text" {
					return nil, errAt(nx, "a value is a number (real or integer); text belongs in a key")
				}
				f.Type = nx.Text
			}
			if t.Text == "key" {
				s.Keys = append(s.Keys, f)
			} else {
				s.Values = append(s.Values, f)
			}
		case "derive":
			ft, err := p.name("a name for the derived value")
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(Assign, "'='"); err != nil {
				return nil, err
			}
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			s.Derived = append(s.Derived, &Derived{Name: ft.Text, Expr: e, Pos: ft.Pos})
		case "raw":
			if s.RawKeep != 0 || s.RawUntilClosed {
				return nil, errAt(t, "raw is declared twice in stream %q", s.Name)
			}
			if p.isWord("until") {
				// raw until closed [+ duration]
				p.next()
				if err := p.word("closed"); err != nil {
					return nil, err
				}
				s.RawUntilClosed, s.RawPos = true, t.Pos
				if p.peek().Kind == Plus {
					p.next()
					d, err := p.duration()
					if err != nil {
						return nil, err
					}
					s.RawAfterClose = d.Secs
				}
				continue
			}
			if err := p.word("keep"); err != nil {
				return nil, err
			}
			k, err := p.keep()
			if err != nil {
				return nil, err
			}
			s.RawKeep, s.RawPos = k, t.Pos
		case "id":
			if s.ID != nil {
				return nil, errAt(t, "id is declared twice in stream %q", s.Name)
			}
			ft, err := p.name("the name of the event's identifier")
			if err != nil {
				return nil, err
			}
			f := &Field{Name: ft.Text, Pos: ft.Pos, Type: "text"}
			if nx := p.peek(); nx.Kind == Ident && (nx.Text == "text" || nx.Text == "integer") {
				p.next()
				f.Type = nx.Text
			}
			if err := p.word("refuse"); err != nil {
				return nil, err
			}
			if err := p.word("repeats"); err != nil {
				return nil, err
			}
			d, err := p.duration()
			if err != nil {
				return nil, err
			}
			s.ID, s.Repeats = f, d.Secs
		case "late":
			if s.Late != 0 {
				return nil, errAt(t, "late is declared twice in stream %q", s.Name)
			}
			d, err := p.duration()
			if err != nil {
				return nil, err
			}
			s.Late = d.Secs
		case "period":
			if s.Period != "" {
				return nil, errAt(t, "period is declared twice in stream %q", s.Name)
			}
			pt := p.next()
			if pt.Kind != Ident || (pt.Text != "hour" && pt.Text != "day" && pt.Text != "month") {
				return nil, errAt(pt, "a period is hour, day or month, not %s", describe(pt))
			}
			if err := p.word("close"); err != nil {
				return nil, err
			}
			ct := p.next()
			if ct.Kind != Duration || ct.Secs < 0 {
				return nil, errAt(ct, "expected how long after its end a period closes, such as 24h, found %s", describe(ct))
			}
			s.Period, s.Close = pt.Text, ct.Secs
		case "rollup":
			rt, err := p.duration()
			if err != nil {
				return nil, err
			}
			if err := p.word("keep"); err != nil {
				return nil, err
			}
			k, err := p.keep()
			if err != nil {
				return nil, err
			}
			r := &Rollup{Res: rt.Secs, Keep: k, Pos: t.Pos}
			if p.isWord("quantiles") {
				p.next()
				for {
					qt, err := p.name("a value name after quantiles")
					if err != nil {
						return nil, err
					}
					r.Quantiles = append(r.Quantiles, qt.Text)
					if p.peek().Kind != Comma {
						break
					}
					p.next()
				}
			}
			s.Rollups = append(s.Rollups, r)
		case "samples":
			if s.Samples != nil {
				return nil, errAt(t, "samples is declared twice in stream %q", s.Name)
			}
			n, nt, err := p.integer("the number of samples")
			if err != nil {
				return nil, err
			}
			if n < 1 || n > 100 {
				return nil, errAt(nt, "keep between 1 and 100 samples per window")
			}
			if err := p.word("per"); err != nil {
				return nil, err
			}
			d, err := p.duration()
			if err != nil {
				return nil, err
			}
			s.Samples = &Samples{N: n, Per: d.Secs, Pos: t.Pos}
		case "anomalies":
			if s.Anomalies != nil {
				return nil, errAt(t, "anomalies is declared twice in stream %q", s.Name)
			}
			a, err := p.anomalies(t)
			if err != nil {
				return nil, err
			}
			s.Anomalies = a
		case "quantile":
			if err := p.word("accuracy"); err != nil {
				return nil, err
			}
			pt, err := p.expect(Percent, "a percentage such as 1%")
			if err != nil {
				return nil, err
			}
			if s.Accuracy != 0 {
				return nil, errAt(t, "quantile accuracy is declared twice in stream %q", s.Name)
			}
			if pt.Num <= 0 || pt.Num >= 0.5 {
				return nil, errAt(pt, "quantile accuracy must be above 0%% and below 50%%")
			}
			s.Accuracy, s.AccPos = pt.Num, t.Pos
		default:
			return nil, errAt(t, "%q is not a stream line; expected key, value, derive, raw, rollup, samples, anomalies, quantile accuracy, or in an exact stream id, late and period", t.Text)
		}
	}
}

func (p *parser) anomalies(start Token) (*Anomalies, error) {
	ft, err := p.name("the value to watch")
	if err != nil {
		return nil, err
	}
	a := &Anomalies{Field: ft.Text, Pos: start.Pos, Memory: 3000, Warmup: 500, Keep: 20}
	for p.isWord("log") || p.isWord("change") {
		t := p.next()
		flag := &a.Log
		if t.Text == "change" {
			flag = &a.Change
		}
		if *flag {
			return nil, errAt(t, "%q is written twice", t.Text)
		}
		*flag = true
	}
	if err := p.word("z"); err != nil {
		return nil, err
	}
	if _, err := p.expect(Greater, "'>'"); err != nil {
		return nil, err
	}
	zt := p.next()
	if zt.Kind != Int && zt.Kind != Number {
		return nil, errAt(zt, "expected a number after z >, found %s", describe(zt))
	}
	if zt.Num <= 0 {
		return nil, errAt(zt, "z must be above zero")
	}
	a.Z = zt.Num
	for {
		switch {
		case p.isWord("memory"):
			p.next()
			n, nt, err := p.integer("the baseline memory")
			if err != nil {
				return nil, err
			}
			if n < 2 {
				return nil, errAt(nt, "memory must be at least 2 events")
			}
			a.Memory = n
		case p.isWord("warmup"):
			p.next()
			n, nt, err := p.integer("the warmup")
			if err != nil {
				return nil, err
			}
			if n < 2 {
				return nil, errAt(nt, "warmup must be at least 2 events")
			}
			a.Warmup = n
		case p.isWord("keep"):
			p.next()
			n, nt, err := p.integer("the number of anomalies to keep")
			if err != nil {
				return nil, err
			}
			if n < 1 {
				return nil, errAt(nt, "keep at least 1 anomaly per window")
			}
			if err := p.word("per"); err != nil {
				return nil, err
			}
			d, err := p.duration()
			if err != nil {
				return nil, err
			}
			a.Keep, a.Per = n, d.Secs
		default:
			return a, nil
		}
	}
}

var quantileFunc = regexp.MustCompile(`^p([0-9]{2}|9[0-9]{2})$`)

func (p *parser) precompute() (*Precompute, error) {
	start := p.next() // precompute
	nt, err := p.name("a name for the precompute")
	if err != nil {
		return nil, err
	}
	pc := &Precompute{Name: nt.Text, Pos: start.Pos}
	if _, err := p.expect(Assign, "'='"); err != nil {
		return nil, err
	}
	ft, err := p.name("a function such as count, avg or p99")
	if err != nil {
		return nil, err
	}
	pc.FuncAs = ft.Text
	switch ft.Text {
	case "count", "sum", "avg", "min", "max", "first", "last":
		pc.Func = ft.Text
	default:
		m := quantileFunc.FindStringSubmatch(ft.Text)
		if m == nil {
			return nil, errAt(ft, "%q is not a function; use count, sum, avg, min, max, first, last, or a percentile such as p50, p95, p99 or p999", ft.Text)
		}
		q, _ := strconv.ParseFloat("0."+m[1], 64)
		if q <= 0 {
			return nil, errAt(ft, "%q asks for the 0th percentile; use min instead", ft.Text)
		}
		pc.Func, pc.Q = "quantile", q
	}
	if _, err := p.expect(LParen, "'('"); err != nil {
		return nil, err
	}
	st, err := p.name("a stream name")
	if err != nil {
		return nil, err
	}
	pc.Stream = st.Text
	if p.peek().Kind == Dot {
		p.next()
		vt, err := p.name("a value name")
		if err != nil {
			return nil, err
		}
		pc.Field = vt.Text
	}
	if _, err := p.expect(RParen, "')'"); err != nil {
		return nil, err
	}
	if p.isWord("by") {
		p.next()
		for {
			kt, err := p.name("a key name after by")
			if err != nil {
				return nil, err
			}
			pc.By = append(pc.By, kt.Text)
			if p.peek().Kind != Comma {
				break
			}
			p.next()
		}
	}
	if p.isWord("per") {
		pt := p.next()
		per := p.next()
		if per.Kind != Ident || (per.Text != "hour" && per.Text != "day" && per.Text != "month") {
			return nil, errAt(pt, "per takes hour, day or month")
		}
		pc.Per = per.Text
	}
	return pc, nil
}

// Expressions: + - * / and parentheses over values and numbers.
func (p *parser) expr() (Expr, error) {
	l, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.peek().Kind == Plus || p.peek().Kind == Minus {
		op := p.next().Text[0]
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		l = Binary{Op: op, L: l, R: r}
	}
	return l, nil
}

func (p *parser) term() (Expr, error) {
	l, err := p.factor()
	if err != nil {
		return nil, err
	}
	for p.peek().Kind == Star || p.peek().Kind == Slash {
		op := p.next().Text[0]
		r, err := p.factor()
		if err != nil {
			return nil, err
		}
		l = Binary{Op: op, L: l, R: r}
	}
	return l, nil
}

func (p *parser) factor() (Expr, error) {
	t := p.next()
	switch t.Kind {
	case Int, Number:
		return Num{Text: t.Text}, nil
	case Ident:
		return Ref{Name: t.Text, Pos: t.Pos}, nil
	case Minus:
		x, err := p.factor()
		if err != nil {
			return nil, err
		}
		return Neg{X: x}, nil
	case LParen:
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(RParen, "')'"); err != nil {
			return nil, err
		}
		return e, nil
	}
	return nil, errAt(t, "expected a value name, a number or '(', found %s", describe(t))
}

// Names become SQL table and column names, so they are kept simple.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// Reserved are words that would clash with SQL or with the columns the compiler adds.
var Reserved = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		abort action add after all alter analyze and as asc attach autoincrement before begin between by
		cascade case cast check collate column commit conflict constraint create cross current current_date
		current_time current_timestamp database default deferrable deferred delete desc detach distinct do
		drop each else end escape except exclusive exists explain fail filter for foreign from full glob
		group having if ignore immediate in index indexed initially inner insert instead intersect into is
		isnull join key left like limit match natural no not nothing notnull null of offset on or order
		outer over partition plan pragma primary query raise range recursive references regexp reindex
		release rename replace restrict returning right rollback row rows savepoint select set table temp
		temporary then to transaction trigger unbounded union unique update using vacuum values view
		virtual when where window with without
		ts res w n an b slot z m var g first_ts last_ts value period text integer real forever
		prev reason newest lim used remaining reached`) {
		Reserved[w] = true
	}
}

func checkName(pos Pos, name, what string) error {
	if !nameRE.MatchString(name) {
		return &Error{pos, fmt.Sprintf("%s %q must start with a lowercase letter and use only lowercase letters, digits and _ (up to 48 characters)", what, name)}
	}
	if Reserved[name] {
		return &Error{pos, fmt.Sprintf("%s %q is a reserved word; pick another name", what, name)}
	}
	return nil
}
