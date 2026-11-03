package otlp

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The JSON encoding of OTLP: lowerCamelCase field names, 64-bit integers as strings or numbers,
// enums as integers, and the special doubles as the strings NaN, Infinity and -Infinity.

type jint int64

func (j *jint) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*j = jint(v)
		return nil
	}
	if v, err := strconv.ParseUint(s, 10, 64); err == nil {
		*j = jint(int64(v))
		return nil
	}
	// A number written with an exponent or a fraction, such as 1.7e18.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > 1<<63 {
		return fmt.Errorf("%s is not a whole number", b)
	}
	*j = jint(int64(f))
	return nil
}

type juint uint64

func (j *juint) UnmarshalJSON(b []byte) error {
	var v jint
	if err := v.UnmarshalJSON(b); err != nil {
		return err
	}
	*j = juint(uint64(v))
	return nil
}

type jfloat float64

func (j *jfloat) UnmarshalJSON(b []byte) error {
	s := string(b)
	switch s {
	case `"NaN"`:
		*j = jfloat(math.NaN())
		return nil
	case `"Infinity"`:
		*j = jfloat(math.Inf(1))
		return nil
	case `"-Infinity"`:
		*j = jfloat(math.Inf(-1))
		return nil
	}
	f, err := strconv.ParseFloat(strings.Trim(s, `"`), 64)
	if err != nil {
		return fmt.Errorf("%s is not a number", b)
	}
	*j = jfloat(f)
	return nil
}

type jAny struct {
	StringValue *string  `json:"stringValue"`
	BoolValue   *bool    `json:"boolValue"`
	IntValue    *jint    `json:"intValue"`
	DoubleValue *jfloat  `json:"doubleValue"`
	ArrayValue  *jValues `json:"arrayValue"`
	KvlistValue *jKVs    `json:"kvlistValue"`
	BytesValue  *string  `json:"bytesValue"`
}

type jValues struct {
	Values []jAny `json:"values"`
}

type jKVs struct {
	Values []jKV `json:"values"`
}

type jKV struct {
	Key   string `json:"key"`
	Value jAny   `json:"value"`
}

func (a *jAny) text(depth int) (string, error) {
	if depth > maxDepth {
		return "", errors.New("attribute values nest too deeply")
	}
	switch {
	case a.StringValue != nil:
		return *a.StringValue, nil
	case a.BoolValue != nil:
		return strconv.FormatBool(*a.BoolValue), nil
	case a.IntValue != nil:
		return strconv.FormatInt(int64(*a.IntValue), 10), nil
	case a.DoubleValue != nil:
		return strconv.FormatFloat(float64(*a.DoubleValue), 'g', -1, 64), nil
	case a.BytesValue != nil:
		b, err := base64.StdEncoding.DecodeString(*a.BytesValue)
		if err != nil {
			// Some senders write bytes in hex, as OTLP/JSON writes trace and span ids.
			if b, err = hex.DecodeString(*a.BytesValue); err != nil {
				return "", errors.New("bytesValue is neither base64 nor hex")
			}
		}
		return base64.StdEncoding.EncodeToString(b), nil
	case a.ArrayValue != nil:
		parts := make([]string, len(a.ArrayValue.Values))
		for i := range a.ArrayValue.Values {
			v, err := a.ArrayValue.Values[i].text(depth + 1)
			if err != nil {
				return "", err
			}
			parts[i] = strconv.Quote(v)
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case a.KvlistValue != nil:
		parts := make([]string, len(a.KvlistValue.Values))
		for i := range a.KvlistValue.Values {
			kv := &a.KvlistValue.Values[i]
			v, err := kv.Value.text(depth + 1)
			if err != nil {
				return "", err
			}
			parts[i] = strconv.Quote(kv.Key) + ":" + strconv.Quote(v)
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	}
	return "", nil
}

func jattrs(list []jKV) ([]KV, error) {
	if len(list) == 0 {
		return nil, nil
	}
	out := make([]KV, 0, len(list))
	for i := range list {
		v, err := list[i].Value.text(0)
		if err != nil {
			return nil, err
		}
		out = append(out, KV{list[i].Key, v})
	}
	return out, nil
}

type jResource struct {
	Attributes []jKV `json:"attributes"`
}

type jScope struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Attributes []jKV  `json:"attributes"`
}

func (s *jScope) attrs() ([]KV, error) {
	var out []KV
	if s == nil {
		return nil, nil
	}
	if s.Name != "" {
		out = append(out, KV{"otel.scope.name", s.Name})
	}
	if s.Version != "" {
		out = append(out, KV{"otel.scope.version", s.Version})
	}
	more, err := jattrs(s.Attributes)
	return append(out, more...), err
}

type jLogs struct {
	ResourceLogs []struct {
		Resource  *jResource `json:"resource"`
		ScopeLogs []struct {
			Scope      *jScope `json:"scope"`
			LogRecords []struct {
				TimeUnixNano         juint  `json:"timeUnixNano"`
				ObservedTimeUnixNano juint  `json:"observedTimeUnixNano"`
				SeverityNumber       jint   `json:"severityNumber"`
				SeverityText         string `json:"severityText"`
				Body                 jAny   `json:"body"`
				Attributes           []jKV  `json:"attributes"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

func strict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("more than one JSON value")
	}
	return nil
}

// DecodeLogsJSON reads an ExportLogsServiceRequest in JSON.
func DecodeLogsJSON(b []byte) ([]LogRecord, error) {
	var req jLogs
	if err := strict(b, &req); err != nil {
		return nil, err
	}
	var out []LogRecord
	for _, rl := range req.ResourceLogs {
		var res []KV
		if rl.Resource != nil {
			var err error
			if res, err = jattrs(rl.Resource.Attributes); err != nil {
				return nil, err
			}
		}
		for _, sl := range rl.ScopeLogs {
			sc, err := sl.Scope.attrs()
			if err != nil {
				return nil, err
			}
			for _, lr := range sl.LogRecords {
				body, err := lr.Body.text(0)
				if err != nil {
					return nil, err
				}
				at, err := jattrs(lr.Attributes)
				if err != nil {
					return nil, err
				}
				out = append(out, LogRecord{Time: uint64(lr.TimeUnixNano), Observed: uint64(lr.ObservedTimeUnixNano),
					SeverityNumber: int64(lr.SeverityNumber), SeverityText: lr.SeverityText, Body: body,
					Resource: res, Scope: sc, Attrs: at})
			}
		}
	}
	return out, nil
}

type jNumberPoint struct {
	Attributes        []jKV   `json:"attributes"`
	StartTimeUnixNano juint   `json:"startTimeUnixNano"`
	TimeUnixNano      juint   `json:"timeUnixNano"`
	AsDouble          *jfloat `json:"asDouble"`
	AsInt             *jint   `json:"asInt"`
	Flags             jint    `json:"flags"`
}

type jHistPoint struct {
	Attributes        []jKV   `json:"attributes"`
	StartTimeUnixNano juint   `json:"startTimeUnixNano"`
	TimeUnixNano      juint   `json:"timeUnixNano"`
	Count             juint   `json:"count"`
	Sum               *jfloat `json:"sum"`
	Min               *jfloat `json:"min"`
	Max               *jfloat `json:"max"`
	Flags             jint    `json:"flags"`
}

type jMetrics struct {
	ResourceMetrics []struct {
		Resource     *jResource `json:"resource"`
		ScopeMetrics []struct {
			Scope   *jScope `json:"scope"`
			Metrics []struct {
				Name  string `json:"name"`
				Gauge *struct {
					DataPoints []jNumberPoint `json:"dataPoints"`
				} `json:"gauge"`
				Sum *struct {
					DataPoints             []jNumberPoint `json:"dataPoints"`
					AggregationTemporality jint           `json:"aggregationTemporality"`
					IsMonotonic            bool           `json:"isMonotonic"`
				} `json:"sum"`
				Histogram *struct {
					DataPoints             []jHistPoint `json:"dataPoints"`
					AggregationTemporality jint         `json:"aggregationTemporality"`
				} `json:"histogram"`
				ExponentialHistogram *struct {
					DataPoints []json.RawMessage `json:"dataPoints"`
				} `json:"exponentialHistogram"`
				Summary *struct {
					DataPoints []json.RawMessage `json:"dataPoints"`
				} `json:"summary"`
			} `json:"metrics"`
		} `json:"scopeMetrics"`
	} `json:"resourceMetrics"`
}

func (p *jNumberPoint) point() (Point, error) {
	at, err := jattrs(p.Attributes)
	out := Point{Attrs: at, Start: uint64(p.StartTimeUnixNano), Time: uint64(p.TimeUnixNano), NoValue: p.Flags&1 != 0}
	switch {
	case p.AsDouble != nil:
		out.Value, out.HasValue = float64(*p.AsDouble), true
	case p.AsInt != nil:
		out.Value, out.HasValue = float64(*p.AsInt), true
	}
	return out, err
}

// DecodeMetricsJSON reads an ExportMetricsServiceRequest in JSON.
func DecodeMetricsJSON(b []byte) ([]Metric, error) {
	var req jMetrics
	if err := strict(b, &req); err != nil {
		return nil, err
	}
	var out []Metric
	for _, rm := range req.ResourceMetrics {
		var res []KV
		if rm.Resource != nil {
			var err error
			if res, err = jattrs(rm.Resource.Attributes); err != nil {
				return nil, err
			}
		}
		for _, sm := range rm.ScopeMetrics {
			sc, err := sm.Scope.attrs()
			if err != nil {
				return nil, err
			}
			for _, jm := range sm.Metrics {
				m := Metric{Name: jm.Name, Resource: res, Scope: sc}
				var nps []jNumberPoint
				switch {
				case jm.Gauge != nil:
					m.Kind, nps = Gauge, jm.Gauge.DataPoints
				case jm.Sum != nil:
					m.Kind, nps = Sum, jm.Sum.DataPoints
					m.Temporality, m.Monotonic = int64(jm.Sum.AggregationTemporality), jm.Sum.IsMonotonic
				case jm.Histogram != nil:
					m.Kind, m.Temporality = Histogram, int64(jm.Histogram.AggregationTemporality)
					for _, hp := range jm.Histogram.DataPoints {
						at, err := jattrs(hp.Attributes)
						if err != nil {
							return nil, err
						}
						p := Point{Attrs: at, Start: uint64(hp.StartTimeUnixNano), Time: uint64(hp.TimeUnixNano),
							Count: float64(hp.Count), NoValue: hp.Flags&1 != 0}
						if hp.Sum != nil {
							p.Sum, p.HasSum = float64(*hp.Sum), true
						}
						if hp.Min != nil {
							p.Min, p.HasMin = float64(*hp.Min), true
						}
						if hp.Max != nil {
							p.Max, p.HasMax = float64(*hp.Max), true
						}
						m.Points = append(m.Points, p)
					}
				case jm.ExponentialHistogram != nil:
					m.Kind, m.Points = ExpHist, make([]Point, len(jm.ExponentialHistogram.DataPoints))
				case jm.Summary != nil:
					m.Kind, m.Points = Summary, make([]Point, len(jm.Summary.DataPoints))
				}
				for i := range nps {
					p, err := nps[i].point()
					if err != nil {
						return nil, err
					}
					m.Points = append(m.Points, p)
				}
				out = append(out, m)
			}
		}
	}
	return out, nil
}
