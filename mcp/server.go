// Package mcp lets AI agents read a Precomputing file over the Model Context Protocol: the
// answers the file keeps ready, summaries of its streams over any time range, and the events it
// keeps whole. Every tool only reads.
//
// The server speaks both eras of the protocol. A request that carries its protocol version in
// _meta (revision 2026-07-28) is served statelessly; a client that opens with initialize gets the
// handshake-based revisions 2025-11-25, 2025-06-18 and 2025-03-26. It holds no sessions: every
// request is answered from the file as it stands. Handle takes one message and is the same for
// the HTTP transport (ServeHTTP), for stdio (ServeStdio) and in the browser.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// HTTP status codes, written out so that the browser build does not need net/http.
const (
	statusOK         = 200
	statusAccepted   = 202
	statusBadRequest = 400
	statusForbidden  = 403
	statusNotFound   = 404
)

// Modern is the protocol revision served statelessly, with per-request metadata.
const Modern = "2026-07-28"

// Legacy lists the handshake-based revisions, newest first.
var Legacy = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// Supported lists every revision the server speaks.
var Supported = append([]string{Modern}, Legacy...)

const (
	metaVersion      = "io.modelcontextprotocol/protocolVersion"
	metaCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo   = "io.modelcontextprotocol/serverInfo"
)

// JSON-RPC and MCP error codes.
const (
	codeParse           = -32700
	codeInvalidRequest  = -32600
	codeMethodNotFound  = -32601
	codeInvalidParams   = -32602
	codeInternal        = -32603
	codeHeaderMismatch  = -32020
	codeUnsupportedVers = -32022
)

// Server answers MCP requests about one Precomputing file.
type Server struct {
	Reader  Reader
	Name    string // the server's name, such as "precomputing"
	Title   string // a name for people, such as "Precomputing: usage.sqlite"
	Version string
	// AllowOrigin decides whether a browser page from this origin may call the HTTP endpoint.
	// Requests without an Origin header are always allowed. nil allows local origins only.
	AllowOrigin func(origin string) bool
}

// Headers gives an HTTP request header by name; nil on stdio and in the browser.
type Headers func(name string) string

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

var nullID = json.RawMessage("null")

// cacheTTL is how long a client may keep the tool list and the discovery result: an hour.
const cacheTTL = 3600000

func (s *Server) info() map[string]any {
	return map[string]any{"name": s.Name, "title": s.Title, "version": s.Version}
}

// Instructions tells a model how to use the server.
const Instructions = "This server reads one Precomputing file: answers kept current as events arrived, " +
	"summaries of each stream by time window, and events kept whole for a while. Start with describe_file, " +
	"which names the streams, keys, answers and time span. get_answer and get_windows read a few kept rows, " +
	"so prefer them to query. Times are UTC unless a time you pass carries an offset."

func reply(id json.RawMessage, result any) []byte {
	b, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", id, result})
	return b
}

func replyError(id json.RawMessage, code int, msg string, data any) []byte {
	if len(id) == 0 {
		id = nullID
	}
	b, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   rpcError        `json:"error"`
	}{"2.0", id, rpcError{code, msg, data}})
	return b
}

// decodeHeader reads a header value that may use the =?base64?...?= form.
func decodeHeader(v string) (string, bool) {
	if strings.HasPrefix(v, "=?base64?") && strings.HasSuffix(v, "?=") && len(v) >= len("=?base64??=") {
		b, err := base64.StdEncoding.DecodeString(v[len("=?base64?") : len(v)-2])
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	return v, true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Handle answers one message, or a batch of them from a client of revision 2025-03-26. It
// returns the HTTP status to answer with and the body, which is empty for a notification.
func (s *Server) Handle(body []byte, h Headers) (int, []byte) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if h != nil && h("MCP-Protocol-Version") != "" && h("MCP-Protocol-Version") != "2025-03-26" {
			return statusBadRequest, replyError(nil, codeInvalidRequest, "batches are not part of this protocol version; send one message per request", nil)
		}
		var batch []json.RawMessage
		if err := json.Unmarshal(trimmed, &batch); err != nil || len(batch) == 0 {
			return statusBadRequest, replyError(nil, codeParse, "the body is not a JSON-RPC message", nil)
		}
		var out [][]byte
		for _, m := range batch {
			_, b := s.handleOne(m, h, true)
			if len(b) > 0 {
				out = append(out, b)
			}
		}
		if len(out) == 0 {
			return statusAccepted, nil
		}
		return statusOK, append(append([]byte("["), bytes.Join(out, []byte(","))...), ']')
	}
	return s.handleOne(trimmed, h, false)
}

func (s *Server) handleOne(body []byte, h Headers, inBatch bool) (int, []byte) {
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		return statusBadRequest, replyError(nil, codeParse, "the body is not a JSON-RPC message", nil)
	}
	isRequest := len(m.ID) > 0 && !bytes.Equal(m.ID, nullID)
	if m.JSONRPC != "2.0" || (m.Method == "" && len(m.Result) == 0 && len(m.Error) == 0) {
		return statusBadRequest, replyError(m.ID, codeInvalidRequest, `a JSON-RPC 2.0 message needs "jsonrpc": "2.0" and a method`, nil)
	}
	if len(m.ID) > 0 && bytes.Equal(m.ID, nullID) {
		return statusBadRequest, replyError(nil, codeInvalidRequest, "a request id must not be null", nil)
	}
	if m.Method == "" { // a response from the client; this server sends no requests
		return statusAccepted, nil
	}
	var params map[string]any
	if len(m.Params) > 0 && !bytes.Equal(m.Params, nullID) {
		if err := json.Unmarshal(m.Params, &params); err != nil {
			return statusBadRequest, replyError(m.ID, codeInvalidParams, "params must be an object", nil)
		}
	}
	meta, _ := params["_meta"].(map[string]any)
	version, _ := meta[metaVersion].(string)
	if version != "" && !inBatch {
		return s.modern(m, params, meta, version, isRequest, h)
	}
	return s.legacy(m, params, isRequest, h)
}

// modern serves a request that carries its protocol version (2026-07-28 and later): each one is
// checked and answered on its own.
func (s *Server) modern(m message, params, meta map[string]any, version string, isRequest bool, h Headers) (int, []byte) {
	bad := func(code int, msg string, data any) (int, []byte) {
		return statusBadRequest, replyError(m.ID, code, msg, data)
	}
	if h != nil {
		hv := h("MCP-Protocol-Version")
		if hv == "" {
			return bad(codeHeaderMismatch, "Header mismatch: the MCP-Protocol-Version header is missing", nil)
		}
		if hv != version {
			return bad(codeHeaderMismatch, fmt.Sprintf("Header mismatch: MCP-Protocol-Version header value '%s' does not match body value '%s'", hv, version), nil)
		}
	}
	if version != Modern {
		return bad(codeUnsupportedVers, "Unsupported protocol version", map[string]any{"supported": Supported, "requested": version})
	}
	if _, ok := meta[metaCapabilities].(map[string]any); !ok {
		return bad(codeInvalidParams, "Invalid params: _meta must include "+metaCapabilities, nil)
	}
	if h != nil {
		hm := h("Mcp-Method")
		if hm == "" {
			return bad(codeHeaderMismatch, "Header mismatch: the Mcp-Method header is missing", nil)
		}
		if hm != m.Method {
			return bad(codeHeaderMismatch, fmt.Sprintf("Header mismatch: Mcp-Method header value '%s' does not match body value '%s'", hm, m.Method), nil)
		}
		if m.Method == "tools/call" {
			name, _ := params["name"].(string)
			hn, ok := decodeHeader(h("Mcp-Name"))
			if h("Mcp-Name") == "" {
				return bad(codeHeaderMismatch, "Header mismatch: the Mcp-Name header is missing", nil)
			}
			if !ok || hn != name {
				return bad(codeHeaderMismatch, fmt.Sprintf("Header mismatch: Mcp-Name header value '%s' does not match body value '%s'", h("Mcp-Name"), name), nil)
			}
		}
	}
	if !isRequest {
		return statusAccepted, nil
	}
	complete := func(r map[string]any) (int, []byte) {
		r["resultType"] = "complete"
		meta, _ := r["_meta"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		meta[metaServerInfo] = s.info()
		r["_meta"] = meta
		return statusOK, reply(m.ID, r)
	}
	switch m.Method {
	case "server/discover":
		return complete(map[string]any{
			"supportedVersions": Supported,
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      Instructions,
			"ttlMs":             cacheTTL,
			"cacheScope":        "public",
		})
	case "ping":
		return complete(map[string]any{})
	case "tools/list":
		// The same five tools for every caller, for as long as the server runs.
		return complete(map[string]any{"tools": tools, "ttlMs": cacheTTL, "cacheScope": "public"})
	case "tools/call":
		res, err := s.call(params)
		if err != nil {
			return statusOK, replyError(m.ID, err.Code, err.Message, err.Data)
		}
		return complete(res)
	}
	return statusNotFound, replyError(m.ID, codeMethodNotFound, "Method not found: "+m.Method, nil)
}

// legacy serves the handshake-based revisions. The server keeps no session: initialize only
// agrees on a version, and every later request is answered from the file.
func (s *Server) legacy(m message, params map[string]any, isRequest bool, h Headers) (int, []byte) {
	if h != nil {
		if hv := h("MCP-Protocol-Version"); hv != "" && !contains(Legacy, hv) {
			if hv == Modern {
				return statusBadRequest, replyError(m.ID, codeInvalidParams, "Invalid params: a "+Modern+" request carries "+metaVersion+" and "+metaCapabilities+" in params._meta", nil)
			}
			return statusBadRequest, replyError(m.ID, codeInvalidRequest, "Unsupported protocol version "+hv, map[string]any{"supported": Supported, "requested": hv})
		}
	}
	if !isRequest {
		return statusAccepted, nil // notifications/initialized, notifications/cancelled and the like
	}
	switch m.Method {
	case "initialize":
		want, _ := params["protocolVersion"].(string)
		agreed := Legacy[0]
		if contains(Legacy, want) {
			agreed = want
		}
		return statusOK, reply(m.ID, map[string]any{
			"protocolVersion": agreed,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      s.info(),
			"instructions":    Instructions,
		})
	case "ping":
		return statusOK, reply(m.ID, map[string]any{})
	case "server/discover":
		return statusOK, reply(m.ID, map[string]any{"supportedVersions": Supported, "capabilities": map[string]any{"tools": map[string]any{}},
			"instructions": Instructions, "_meta": map[string]any{metaServerInfo: s.info()}})
	case "tools/list":
		return statusOK, reply(m.ID, map[string]any{"tools": tools})
	case "tools/call":
		res, err := s.call(params)
		if err != nil {
			return statusOK, replyError(m.ID, err.Code, err.Message, err.Data)
		}
		return statusOK, reply(m.ID, res)
	}
	return statusOK, replyError(m.ID, codeMethodNotFound, "Method not found: "+m.Method, nil)
}

// call runs a tool. An unknown tool or malformed params is a protocol error; anything wrong with
// the arguments themselves comes back as a tool result with isError, for the model to correct.
func (s *Server) call(params map[string]any) (map[string]any, *rpcError) {
	name, _ := params["name"].(string)
	t := findTool(name)
	if t == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Unknown tool: " + name}
	}
	var arguments map[string]any
	switch x := params["arguments"].(type) {
	case nil:
		arguments = map[string]any{}
	case map[string]any:
		arguments = x
	default:
		return nil, &rpcError{Code: codeInvalidParams, Message: "arguments must be an object"}
	}
	text, isErr := callTool(s.Reader, t, arguments)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isErr}, nil
}

// ServeStdio is the stdio transport: one JSON-RPC message per line in, one per line out.
func (s *Server) ServeStdio(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	bw := bufio.NewWriter(out)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		_, resp := s.Handle(line, nil)
		if len(resp) == 0 {
			continue
		}
		bw.Write(resp)
		bw.WriteByte('\n')
		if err := bw.Flush(); err != nil {
			return err
		}
	}
	return sc.Err()
}
