package main

import (
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"precomputing.com/precomputing/engine"
	"precomputing.com/precomputing/otlp"
)

// Limits on one OTLP request: what comes over the wire, and what it may unpack to.
const (
	otlpMaxBody     = 16 << 20
	otlpMaxUnpacked = 64 << 20
)

// otlpBody reads a request's body, unpacking gzip, and says whether it is JSON. It writes the
// error reply itself and returns ok false when the request can't be read.
func otlpBody(w http.ResponseWriter, r *http.Request) (body []byte, isJSON, ok bool) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ct {
	case "application/x-protobuf":
	case "application/json":
		isJSON = true
	default:
		http.Error(w, "OTLP/HTTP sends application/x-protobuf or application/json", http.StatusUnsupportedMediaType)
		return nil, false, false
	}
	var rd io.Reader = http.MaxBytesReader(w, r.Body, otlpMaxBody)
	switch r.Header.Get("Content-Encoding") {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(rd)
		if err != nil {
			otlpError(w, isJSON, http.StatusBadRequest, "the body is not gzip: "+err.Error())
			return nil, false, false
		}
		defer gz.Close()
		rd = gz
	default:
		otlpError(w, isJSON, http.StatusUnsupportedMediaType, "Content-Encoding is gzip or none")
		return nil, false, false
	}
	b, err := io.ReadAll(io.LimitReader(rd, otlpMaxUnpacked+1))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			otlpError(w, isJSON, http.StatusRequestEntityTooLarge, fmt.Sprintf("a request is at most %d bytes", otlpMaxBody))
		} else {
			otlpError(w, isJSON, http.StatusBadRequest, "reading the body: "+err.Error())
		}
		return nil, false, false
	}
	if len(b) > otlpMaxUnpacked {
		otlpError(w, isJSON, http.StatusRequestEntityTooLarge, fmt.Sprintf("a request unpacks to at most %d bytes", otlpMaxUnpacked))
		return nil, false, false
	}
	return b, isJSON, true
}

// otlpError replies with a google.rpc.Status, as OTLP/HTTP asks: in protobuf, field 2 is the message.
func otlpError(w http.ResponseWriter, isJSON bool, code int, msg string) {
	if isJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg})
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(code)
	w.Write(protoString(2, msg))
}

func protoString(field int, s string) []byte {
	b := binary.AppendUvarint(nil, uint64(field<<3|2))
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// otlpReply answers an export: an empty response when all of it was taken, or a partial success
// that says how many records or points were rejected and why.
func otlpReply(w http.ResponseWriter, isJSON bool, signal string, res otlp.Result) {
	if isJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if res.Rejected == 0 {
			w.Write([]byte("{}\n"))
			return
		}
		name := map[string]string{"logs": "rejectedLogRecords", "metrics": "rejectedDataPoints"}[signal]
		json.NewEncoder(w).Encode(map[string]any{"partialSuccess": map[string]any{name: fmt.Sprint(res.Rejected), "errorMessage": res.Message}})
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	if res.Rejected == 0 {
		return
	}
	// ExportLogsPartialSuccess and ExportMetricsPartialSuccess: 1 the count, 2 the message.
	ps := binary.AppendUvarint(nil, 1<<3)
	ps = binary.AppendUvarint(ps, uint64(res.Rejected))
	ps = append(ps, protoString(2, res.Message)...)
	out := binary.AppendUvarint(nil, 1<<3|2)
	out = binary.AppendUvarint(out, uint64(len(ps)))
	w.Write(append(out, ps...))
}

// otlpDecode decodes a request of either signal.
func otlpDecode(signal string, body []byte, isJSON bool) ([]otlp.LogRecord, []otlp.Metric, error) {
	switch {
	case signal == "logs" && isJSON:
		r, err := otlp.DecodeLogsJSON(body)
		return r, nil, err
	case signal == "logs":
		r, err := otlp.DecodeLogs(body)
		return r, nil, err
	case isJSON:
		m, err := otlp.DecodeMetricsJSON(body)
		return nil, m, err
	}
	m, err := otlp.DecodeMetrics(body)
	return nil, m, err
}

// otlpTake decodes and takes one request under the Engine's lock, which the caller holds.
func otlpTake(in *otlp.Intake, signal string, body []byte, isJSON bool) (otlp.Result, int, error) {
	recs, ms, err := otlpDecode(signal, body, isJSON)
	if err != nil {
		return otlp.Result{}, http.StatusBadRequest, fmt.Errorf("not an OTLP %s request: %v", signal, err)
	}
	var res otlp.Result
	if signal == "logs" {
		res, err = in.Logs(body, recs)
	} else {
		res, err = in.Metrics(body, ms)
	}
	if err != nil {
		var stopped *engine.StoppedError
		if errors.As(err, &stopped) {
			return res, http.StatusServiceUnavailable, err
		}
		return res, http.StatusInternalServerError, err
	}
	return res, http.StatusOK, nil
}

func otlpStats(s *otlp.Stats) any {
	if s == nil {
		return nil
	}
	return map[string]int64{"requests": s.Requests, "repeats": s.Repeats, "records": s.Records, "points": s.Points,
		"events": s.Events, "unused": s.Unused, "baseline": s.Baseline, "rejected": s.Rejected}
}
