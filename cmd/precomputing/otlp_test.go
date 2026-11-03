package main

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"precomputing.com/precomputing/otlp"
)

func TestOTLPBody(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(`{"resourceLogs":[]}`))
	zw.Close()
	for _, c := range []struct {
		ct, ce string
		body   []byte
		code   int
		json   bool
	}{
		{"application/x-protobuf", "", []byte{}, 0, false},
		{"application/json; charset=utf-8", "gzip", gz.Bytes(), 0, true},
		{"text/plain", "", nil, http.StatusUnsupportedMediaType, false},
		{"application/json", "br", nil, http.StatusUnsupportedMediaType, true},
		{"application/json", "gzip", []byte("not gzip"), http.StatusBadRequest, true},
	} {
		req := httptest.NewRequest("POST", "/v1/logs", bytes.NewReader(c.body))
		req.Header.Set("Content-Type", c.ct)
		req.Header.Set("Content-Encoding", c.ce)
		w := httptest.NewRecorder()
		body, isJSON, ok := otlpBody(w, req)
		if c.code == 0 {
			if !ok || isJSON != c.json || (c.json && string(body) != `{"resourceLogs":[]}`) {
				t.Errorf("%s %s: %q %v %v", c.ct, c.ce, body, isJSON, ok)
			}
		} else if ok || w.Code != c.code {
			t.Errorf("%s %s: code %d, want %d", c.ct, c.ce, w.Code, c.code)
		}
	}
}

func TestOTLPReply(t *testing.T) {
	w := httptest.NewRecorder()
	otlpReply(w, false, "metrics", otlp.Result{Rejected: 3, Message: "x is a summary"})
	// ExportMetricsServiceResponse { partial_success (1) { rejected_data_points (1) = 3, error_message (2) } }
	want := []byte{0x0a, 0x12, 0x08, 0x03, 0x12, 0x0e}
	want = append(want, "x is a summary"...)
	if !bytes.Equal(w.Body.Bytes(), want) || w.Header().Get("Content-Type") != "application/x-protobuf" {
		t.Errorf("protobuf partial success: % x", w.Body.Bytes())
	}
	w = httptest.NewRecorder()
	otlpReply(w, true, "logs", otlp.Result{Rejected: 1, Message: "no time"})
	if got := strings.TrimSpace(w.Body.String()); got != `{"partialSuccess":{"errorMessage":"no time","rejectedLogRecords":"1"}}` {
		t.Errorf("JSON partial success: %s", got)
	}
	w = httptest.NewRecorder()
	otlpReply(w, false, "logs", otlp.Result{})
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("full success: %d %q", w.Code, w.Body.String())
	}
}
