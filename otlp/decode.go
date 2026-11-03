// Package otlp takes OpenTelemetry logs and metrics, as OTLP/HTTP sends them, into an Engine.
// It decodes the protobuf and JSON encodings by hand, so the binary needs nothing outside the
// standard library, and reads only the fields it uses: everything else is skipped.
package otlp

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// KV is an attribute with its value as text.
type KV struct{ Key, Value string }

// LogRecord is one OTLP log record with the attributes of its resource and scope.
type LogRecord struct {
	Time, Observed uint64 // nanoseconds since 1970
	SeverityNumber int64
	SeverityText   string
	Body           string
	Resource       []KV
	Scope          []KV
	Attrs          []KV
}

// Point is one data point of a metric.
type Point struct {
	Attrs       []KV
	Start, Time uint64 // nanoseconds since 1970
	Value       float64
	HasValue    bool    // a gauge or a sum point with a value
	Count       float64 // histogram points
	Sum         float64
	Min, Max    float64
	HasSum      bool
	HasMin      bool
	HasMax      bool
	NoValue     bool // the flag that says the point has no recorded value
}

// Kinds of metrics.
const (
	Gauge     = "gauge"
	Sum       = "sum"
	Histogram = "histogram"
	ExpHist   = "exponential histogram"
	Summary   = "summary"
)

// Temporality of sums and histograms.
const (
	Unspecified = 0
	Delta       = 1
	Cumulative  = 2
)

// MaxItems is the most log records, or metric points, one request may hold. Exporters send a few
// thousand at a time; the limit keeps a small request of empty records from filling memory.
const MaxItems = 200000

// MaxMessages is the most messages of any kind one request may hold: records, points, metrics,
// scopes, resources, attributes and their values. Every one costs memory once decoded, so empty
// ones can't be used to fill it.
const MaxMessages = 400000

var errTooMany = fmt.Errorf("a request holds more than %d records or points, or more than %d messages in all", MaxItems, MaxMessages)

// dec decodes one request, counting the messages it reads.
type dec struct{ messages int }

// Metric is one OTLP metric with the attributes of its resource and scope.
type Metric struct {
	Name        string
	Kind        string
	Unread      int // points of kinds Precomputing does not take: counted, not read
	Temporality int64
	Monotonic   bool
	Resource    []KV
	Scope       []KV
	Points      []Point
}

// maxDepth bounds how deeply attribute values (arrays and maps) may nest.
const maxDepth = 16

var errTruncated = errors.New("the message ends in the middle of a field")

// wire reads protobuf fields one at a time.
type wire struct {
	b []byte
	i int
}

// Wire types.
const (
	wtVarint  = 0
	wtFixed64 = 1
	wtBytes   = 2
	wtFixed32 = 5
)

func (w *wire) more() bool { return w.i < len(w.b) }

func (w *wire) uvarint() (uint64, error) {
	v, n := binary.Uvarint(w.b[w.i:])
	if n <= 0 {
		return 0, errTruncated
	}
	w.i += n
	return v, nil
}

// field reads the next field's number and wire type.
func (w *wire) field() (int, int, error) {
	v, err := w.uvarint()
	if err != nil {
		return 0, 0, err
	}
	if v>>3 == 0 || v>>3 > math.MaxInt32 {
		return 0, 0, fmt.Errorf("field number %d", v>>3)
	}
	return int(v >> 3), int(v & 7), nil
}

func (w *wire) fixed64() (uint64, error) {
	if len(w.b)-w.i < 8 {
		return 0, errTruncated
	}
	v := binary.LittleEndian.Uint64(w.b[w.i:])
	w.i += 8
	return v, nil
}

func (w *wire) bytes() ([]byte, error) {
	n, err := w.uvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(w.b)-w.i) {
		return nil, errTruncated
	}
	b := w.b[w.i : w.i+int(n)]
	w.i += int(n)
	return b, nil
}

// skip passes over a field of the given wire type.
func (w *wire) skip(wt int) error {
	switch wt {
	case wtVarint:
		_, err := w.uvarint()
		return err
	case wtFixed64:
		_, err := w.fixed64()
		return err
	case wtBytes:
		_, err := w.bytes()
		return err
	case wtFixed32:
		if len(w.b)-w.i < 4 {
			return errTruncated
		}
		w.i += 4
		return nil
	}
	return fmt.Errorf("wire type %d is not one OTLP uses", wt)
}

// each calls fn for every field of a message; fn reads the field or returns false to skip it.
func (d *dec) each(b []byte, fn func(w *wire, num, wt int) (bool, error)) error {
	if d.messages++; d.messages > MaxMessages {
		return errTooMany
	}
	w := &wire{b: b}
	for w.more() {
		num, wt, err := w.field()
		if err != nil {
			return err
		}
		read, err := fn(w, num, wt)
		if err != nil {
			return err
		}
		if !read {
			if err := w.skip(wt); err != nil {
				return err
			}
		}
	}
	return nil
}

// sub reads a field that holds a message.
func sub(w *wire, wt int) ([]byte, error) {
	if wt != wtBytes {
		return nil, fmt.Errorf("a message field has wire type %d", wt)
	}
	return w.bytes()
}

func text(w *wire, wt int) (string, error) {
	b, err := sub(w, wt)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", errors.New("a string is not UTF-8")
	}
	return string(b), nil
}

func fix64(w *wire, wt int) (uint64, error) {
	if wt != wtFixed64 {
		return 0, fmt.Errorf("a fixed64 field has wire type %d", wt)
	}
	return w.fixed64()
}

func vint(w *wire, wt int) (uint64, error) {
	if wt != wtVarint {
		return 0, fmt.Errorf("a varint field has wire type %d", wt)
	}
	return w.uvarint()
}

func double(w *wire, wt int) (float64, error) {
	v, err := fix64(w, wt)
	return math.Float64frombits(v), err
}

// MaxValue is the longest an array or a map may be as JSON text. Plain strings are as long as
// they came, which the request's size bounds.
const MaxValue = 64 << 10

var errLongValue = fmt.Errorf("an array or map attribute value is longer than %d bytes as JSON", MaxValue)

// anyValue reads an AnyValue as text: strings as they are, numbers and booleans as Go prints them,
// bytes in base64, arrays and maps as JSON.
func (d *dec) anyValue(b []byte, depth int) (string, error) {
	s, _, err := d.anyValueJSON(b, depth)
	return s, err
}

// anyValueJSON also says whether the text is an array or a map written as JSON, which a
// surrounding array or map takes as it is rather than as a quoted string. Quoting nested JSON again
// at every level would double its length each time.
func (d *dec) anyValueJSON(b []byte, depth int) (string, bool, error) {
	if depth > maxDepth {
		return "", false, errors.New("attribute values nest too deeply")
	}
	out, nested := "", false
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		var err error
		nested = false
		switch num {
		case 1:
			out, err = text(w, wt)
		case 2:
			var v uint64
			v, err = vint(w, wt)
			out = strconv.FormatBool(v != 0)
		case 3:
			var v uint64
			v, err = vint(w, wt)
			out = strconv.FormatInt(int64(v), 10)
		case 4:
			var v float64
			v, err = double(w, wt)
			out = strconv.FormatFloat(v, 'g', -1, 64)
		case 5, 6:
			var m []byte
			if m, err = sub(w, wt); err == nil {
				out, err = d.composite(m, num == 6, depth+1)
				nested = true
			}
		case 7:
			var v []byte
			v, err = sub(w, wt)
			out = base64.StdEncoding.EncodeToString(v)
		default:
			return false, nil
		}
		return true, err
	})
	return out, nested, err
}

// jsonPart is an element of an array or map in JSON: nested JSON as it is, anything else quoted.
func jsonPart(s string, nested bool) string {
	if nested {
		return s
	}
	return strconv.Quote(s)
}

// composite writes an ArrayValue or a KeyValueList as JSON.
func (d *dec) composite(b []byte, kv bool, depth int) (string, error) {
	var sb strings.Builder
	n := 0
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		if num != 1 {
			return false, nil
		}
		m, err := sub(w, wt)
		if err != nil {
			return true, err
		}
		if n > 0 {
			sb.WriteByte(',')
		}
		n++
		if kv {
			var key, val string
			var nested bool
			err := d.each(m, func(w *wire, num, wt int) (bool, error) {
				var err error
				switch num {
				case 1:
					key, err = text(w, wt)
				case 2:
					var v []byte
					if v, err = sub(w, wt); err == nil {
						val, nested, err = d.anyValueJSON(v, depth)
					}
				default:
					return false, nil
				}
				return true, err
			})
			if err != nil {
				return true, err
			}
			sb.WriteString(strconv.Quote(key) + ":" + jsonPart(val, nested))
		} else {
			v, nested, err := d.anyValueJSON(m, depth)
			if err != nil {
				return true, err
			}
			sb.WriteString(jsonPart(v, nested))
		}
		if sb.Len() > MaxValue {
			return true, errLongValue
		}
		return true, nil
	})
	if kv {
		return "{" + sb.String() + "}", err
	}
	return "[" + sb.String() + "]", err
}

func (d *dec) keyValue(b []byte, depth int) (KV, error) {
	var kv KV
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		var err error
		switch num {
		case 1:
			kv.Key, err = text(w, wt)
		case 2:
			var m []byte
			if m, err = sub(w, wt); err == nil {
				kv.Value, err = d.anyValue(m, depth)
			}
		default:
			return false, nil
		}
		return true, err
	})
	return kv, err
}

// attrs reads a repeated KeyValue field into list.
func (d *dec) attrs(w *wire, wt int, list *[]KV) error {
	m, err := sub(w, wt)
	if err != nil {
		return err
	}
	kv, err := d.keyValue(m, 0)
	if err != nil {
		return err
	}
	*list = append(*list, kv)
	return nil
}

// resource reads a Resource: its attributes.
func (d *dec) resource(b []byte) ([]KV, error) {
	var out []KV
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		if num != 1 {
			return false, nil
		}
		return true, d.attrs(w, wt, &out)
	})
	return out, err
}

// scope reads an InstrumentationScope as attributes: otel.scope.name and version, then its own.
func (d *dec) scope(b []byte) ([]KV, error) {
	var out []KV
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		var err error
		switch num {
		case 1, 2:
			var s string
			if s, err = text(w, wt); err == nil && s != "" {
				name := "otel.scope.name"
				if num == 2 {
					name = "otel.scope.version"
				}
				out = append(out, KV{name, s})
			}
		case 3:
			err = d.attrs(w, wt, &out)
		default:
			return false, nil
		}
		return true, err
	})
	return out, err
}

// DecodeLogs reads an ExportLogsServiceRequest in protobuf.
func DecodeLogs(b []byte) ([]LogRecord, error) { return (&dec{}).logs(b) }

func (d *dec) logs(b []byte) ([]LogRecord, error) {
	var out []LogRecord
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		if num != 1 {
			return false, nil
		}
		rl, err := sub(w, wt)
		if err != nil {
			return true, err
		}
		var res []KV
		var scopes [][]byte
		err = d.each(rl, func(w *wire, num, wt int) (bool, error) {
			var err error
			switch num {
			case 1:
				var m []byte
				if m, err = sub(w, wt); err == nil {
					res, err = d.resource(m)
				}
			case 2:
				var m []byte
				if m, err = sub(w, wt); err == nil {
					if d.messages++; d.messages > MaxMessages {
						return true, errTooMany
					}
					scopes = append(scopes, m)
				}
			default:
				return false, nil
			}
			return true, err
		})
		if err != nil {
			return true, err
		}
		for _, sl := range scopes {
			var sc []KV
			var recs [][]byte
			err := d.each(sl, func(w *wire, num, wt int) (bool, error) {
				var err error
				switch num {
				case 1:
					var m []byte
					if m, err = sub(w, wt); err == nil {
						sc, err = d.scope(m)
					}
				case 2:
					var m []byte
					if m, err = sub(w, wt); err == nil {
						if d.messages++; d.messages > MaxMessages {
							return true, errTooMany
						}
						recs = append(recs, m)
					}
				default:
					return false, nil
				}
				return true, err
			})
			if err != nil {
				return true, err
			}
			for _, rec := range recs {
				r := LogRecord{Resource: res, Scope: sc}
				err := d.each(rec, func(w *wire, num, wt int) (bool, error) {
					var err error
					switch num {
					case 1:
						r.Time, err = fix64(w, wt)
					case 11:
						r.Observed, err = fix64(w, wt)
					case 2:
						var v uint64
						v, err = vint(w, wt)
						r.SeverityNumber = int64(int32(v))
					case 3:
						r.SeverityText, err = text(w, wt)
					case 5:
						var m []byte
						if m, err = sub(w, wt); err == nil {
							r.Body, err = d.anyValue(m, 0)
						}
					case 6:
						err = d.attrs(w, wt, &r.Attrs)
					default:
						return false, nil
					}
					return true, err
				})
				if err != nil {
					return true, err
				}
				if len(out) >= MaxItems {
					return true, errTooMany
				}
				out = append(out, r)
			}
		}
		return true, nil
	})
	return out, err
}

// DecodeMetrics reads an ExportMetricsServiceRequest in protobuf.
func DecodeMetrics(b []byte) ([]Metric, error) { return (&dec{}).metrics(b) }

func (d *dec) metrics(b []byte) ([]Metric, error) {
	var out []Metric
	points := 0
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		if num != 1 {
			return false, nil
		}
		rm, err := sub(w, wt)
		if err != nil {
			return true, err
		}
		var res []KV
		var scopes [][]byte
		err = d.each(rm, func(w *wire, num, wt int) (bool, error) {
			var err error
			switch num {
			case 1:
				var m []byte
				if m, err = sub(w, wt); err == nil {
					res, err = d.resource(m)
				}
			case 2:
				var m []byte
				if m, err = sub(w, wt); err == nil {
					if d.messages++; d.messages > MaxMessages {
						return true, errTooMany
					}
					scopes = append(scopes, m)
				}
			default:
				return false, nil
			}
			return true, err
		})
		if err != nil {
			return true, err
		}
		for _, sm := range scopes {
			var sc []KV
			var ms [][]byte
			err := d.each(sm, func(w *wire, num, wt int) (bool, error) {
				var err error
				switch num {
				case 1:
					var m []byte
					if m, err = sub(w, wt); err == nil {
						sc, err = d.scope(m)
					}
				case 2:
					var m []byte
					if m, err = sub(w, wt); err == nil {
						if d.messages++; d.messages > MaxMessages {
							return true, errTooMany
						}
						ms = append(ms, m)
					}
				default:
					return false, nil
				}
				return true, err
			})
			if err != nil {
				return true, err
			}
			for _, mb := range ms {
				m, err := d.metric(mb, &points)
				if err != nil {
					return true, err
				}
				m.Resource, m.Scope = res, sc
				out = append(out, m)
			}
		}
		return true, nil
	})
	return out, err
}

func (d *dec) metric(b []byte, points *int) (Metric, error) {
	var m Metric
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		var err error
		switch num {
		case 1:
			m.Name, err = text(w, wt)
		case 5, 7, 9, 10, 11:
			// A metric holds one kind; should a message carry two, the last one counts.
			m.Kind = map[int]string{5: Gauge, 7: Sum, 9: Histogram, 10: ExpHist, 11: Summary}[num]
			m.Points, m.Unread = nil, 0
			var data []byte
			if data, err = sub(w, wt); err == nil {
				err = d.metricData(data, &m, num, points)
			}
		default:
			return false, nil
		}
		return true, err
	})
	return m, err
}

// metricData reads a Gauge, Sum or Histogram. The other kinds are kept with their points left out:
// Precomputing does not take them, and says so.
func (d *dec) metricData(b []byte, m *Metric, kind int, points *int) error {
	return d.each(b, func(w *wire, num, wt int) (bool, error) {
		var err error
		switch {
		case num == 1 && (kind == 5 || kind == 7 || kind == 9):
			var data []byte
			if data, err = sub(w, wt); err == nil {
				var p Point
				if kind == 9 {
					p, err = d.histogramPoint(data)
				} else {
					p, err = d.numberPoint(data)
				}
				if *points++; *points > MaxItems {
					return true, errTooMany
				}
				m.Points = append(m.Points, p)
			}
		case num == 1:
			if _, err = sub(w, wt); err == nil {
				if *points++; *points > MaxItems {
					return true, errTooMany
				}
				m.Unread++
			}
		case num == 2 && kind != 5 && kind != 11:
			var v uint64
			v, err = vint(w, wt)
			m.Temporality = int64(v)
		case num == 3 && kind == 7:
			var v uint64
			v, err = vint(w, wt)
			m.Monotonic = v != 0
		default:
			return false, nil
		}
		return true, err
	})
}

func (d *dec) numberPoint(b []byte) (Point, error) {
	var p Point
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		var err error
		switch num {
		case 7:
			err = d.attrs(w, wt, &p.Attrs)
		case 2:
			p.Start, err = fix64(w, wt)
		case 3:
			p.Time, err = fix64(w, wt)
		case 4:
			p.Value, err = double(w, wt)
			p.HasValue = true
		case 6:
			var v uint64
			v, err = fix64(w, wt)
			p.Value, p.HasValue = float64(int64(v)), true
		case 8:
			var v uint64
			v, err = vint(w, wt)
			p.NoValue = v&1 != 0
		default:
			return false, nil
		}
		return true, err
	})
	return p, err
}

func (d *dec) histogramPoint(b []byte) (Point, error) {
	var p Point
	err := d.each(b, func(w *wire, num, wt int) (bool, error) {
		var err error
		switch num {
		case 9:
			err = d.attrs(w, wt, &p.Attrs)
		case 2:
			p.Start, err = fix64(w, wt)
		case 3:
			p.Time, err = fix64(w, wt)
		case 4:
			var v uint64
			v, err = fix64(w, wt)
			p.Count = float64(v)
		case 5:
			p.Sum, err = double(w, wt)
			p.HasSum = true
		case 11:
			p.Min, err = double(w, wt)
			p.HasMin = true
		case 12:
			p.Max, err = double(w, wt)
			p.HasMax = true
		case 10:
			var v uint64
			v, err = vint(w, wt)
			p.NoValue = v&1 != 0
		default:
			return false, nil
		}
		return true, err
	})
	return p, err
}
