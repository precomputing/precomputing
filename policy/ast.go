package policy

// Policy is a whole .precompute file.
type Policy struct {
	Streams     []*Stream
	Precomputes []*Precompute
	Quotas      []*Quota
	Logs        *Logs  // how log lines are read, for streams from logs
	Source      string // the file's text, kept in the compiled file
}

// Logs says how log lines are read and how their templates are learned, for streams fed from logs.
type Logs struct {
	Format     string   // e.g. "<timestamp> <level> <service> <message>"
	Fields     []string // the fields the format names, in order
	Masks      []string // regular expressions masked in the message before templates are learned
	Similarity float64  // share of a template's tokens a message must match to join it
	Depth      int      // depth of the template tree
	Pos        Pos
	FormatPos  Pos
	MaskPos    []Pos
}

// Cond is one condition of a stream's where: a log field equals a text.
type Cond struct {
	Field, Value string
	Pos          Pos
}

// Stream is a sequence of timestamped events with the same fields.
type Stream struct {
	Name      string
	Pos       Pos
	Keys      []*Field // what events are grouped by, e.g. endpoint
	Values    []*Field // the measured numbers, e.g. ms
	Derived   []*Derived
	RawKeep   int64 // seconds the raw events stay; 0 = not kept; -1 = forever
	RawPos    Pos
	Rollups   []*Rollup
	Samples   *Samples
	Anomalies *Anomalies
	Accuracy  float64 // relative accuracy of quantile sketches, e.g. 0.01
	AccPos    Pos

	// Exact streams keep every event until the period it belongs to has closed. For usage that
	// is billed: answers are exact, repeats are refused and closed periods never change.
	Exact          bool
	ID             *Field // the event's own identifier, given by the sender
	Repeats        int64  // seconds an identifier is remembered, so a repeat is refused
	Late           int64  // an event older than the newest by more than this is refused; 0 = any
	Period         string // "hour", "day" or "month": the billing period
	Close          int64  // a period closes this long after it ends; its events are then refused
	RawUntilClosed bool   // raw events stay until their period has closed, plus RawAfterClose
	RawAfterClose  int64
	ExactPos       Pos

	// A stream from logs takes its events from log lines: each line that meets Where gives one
	// event, its keys and values read from the line's fields, and the line itself is kept with
	// raw events, samples and anomalies.
	FromLogs bool
	Where    []Cond
	FromPos  Pos
}

// Quota puts a limit on a sum or count precompute, per key and period. The limits live in a
// table; a view shows what is used, the limit and what remains.
type Quota struct {
	Name       string
	Precompute string
	Pos        Pos
}

// Field is a key or a value of a stream.
type Field struct {
	Name string
	Type string // "text" or "integer" for keys; "real" or "integer" for values
	Pos  Pos
}

// Derived is a value computed from other values of the same event.
type Derived struct {
	Name string
	Expr Expr
	Pos  Pos
}

// Rollup keeps window summaries at one resolution.
type Rollup struct {
	Res       int64 // window length in seconds
	Keep      int64 // seconds the windows stay; -1 = forever
	Quantiles []string
	Pos       Pos
}

// Samples keeps a few whole events per window, chosen evenly from all of them.
type Samples struct {
	N   int
	Per int64
	Pos Pos
}

// Anomalies keeps unusual events whole, judged against a running baseline.
type Anomalies struct {
	Field  string
	Log    bool    // judge the logarithm of the value (right for latencies and prices)
	Change bool    // judge the step from the key's previous event instead of the level (right for price jumps)
	Z      float64 // how many standard deviations from the baseline counts as unusual
	Memory int     // the baseline follows about the last Memory events
	Warmup int     // events seen before any is judged
	Keep   int     // exemplars kept per window of length Per
	Per    int64
	Pos    Pos
}

// Precompute is an answer kept ready as events arrive.
type Precompute struct {
	Name   string
	Func   string  // count, sum, avg, min, max, first, last, quantile
	Q      float64 // for quantile, e.g. 0.99
	FuncAs string  // the function as written, e.g. p99
	Stream string
	Field  string // empty for count(stream)
	By     []string
	Per    string // "", "hour", "day" or "month"
	Pos    Pos
}

// Expr is an arithmetic expression over the values of one event.
type Expr interface{ exprNode() }

// Num is a number in an expression.
type Num struct{ Text string }

// Ref is a value name in an expression.
type Ref struct {
	Name string
	Pos  Pos
}

// Binary is a op b.
type Binary struct {
	Op   byte // + - * /
	L, R Expr
}

// Neg is -x.
type Neg struct{ X Expr }

func (Num) exprNode()    {}
func (Ref) exprNode()    {}
func (Binary) exprNode() {}
func (Neg) exprNode()    {}
