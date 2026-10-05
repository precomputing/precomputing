// Package traces keeps the model calls of AI agent runs. An agent sends the whole conversation
// again with every call, so a run's calls repeat everything that came before them. The store keeps
// each piece of a call once, a message or the tool list, under the SHA-256 of its bytes, and each
// call as the list of its pieces. Any call can be rebuilt from the file byte for byte, in SQL, and
// secrets are masked before anything is stored.
//
// Every call is also metered: its tokens and its cost go to the Engine's exact streams, one event
// for the call and one for each source of its input, so a policy can keep cost by run, repository,
// model and tool, with budgets as quotas.
package traces

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Message is one message of a run, as the agent sent it.
type Message struct {
	Raw    string `json:"raw"`    // the message as JSON, as sent
	Role   string `json:"role"`   // system, user, assistant or tool
	Source string `json:"source"` // system, task, user, assistant, or tool:NAME for a tool's output
	Tokens int64  `json:"tokens"`
	At     int64  `json:"at"` // seconds after the run started
}

// Run is one agent run: its messages in order and the tool list it sent with every call.
type Run struct {
	ID          string    `json:"id"`
	Repo        string    `json:"repo"`
	Task        string    `json:"task"`
	Model       string    `json:"model"`
	Start       int64     `json:"start"`
	Resolved    bool      `json:"resolved"`
	Tools       string    `json:"tools"` // the tool list as JSON, as sent
	ToolsTokens int64     `json:"tools_tokens"`
	Messages    []Message `json:"messages"`
}

// ParseRun reads one run from a JSON line.
func ParseRun(line []byte) (*Run, error) {
	var r Run
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("a run is not readable: %v", err)
	}
	if r.ID == "" || len(r.Messages) == 0 || r.Tools == "" {
		return nil, fmt.Errorf("a run needs an id, messages and a tool list")
	}
	for _, c := range r.Model {
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return nil, fmt.Errorf("run %s: a model name must be plain ASCII without quotes or backslashes", r.ID)
		}
	}
	return &r, nil
}

// quote writes a model name as a JSON string. ParseRun lets through only names that need no
// escaping, so Go, JavaScript and SQLite's json_quote all write them the same way.
func quote(s string) string { return `"` + s + `"` }

// CacheWindow is how long a provider keeps a call's input cached for the next call, in seconds.
const CacheWindow = 300

// Replies lists the messages that answer a model call: every assistant message after the first
// message. Call number k (from 1) is answered by Replies()[k-1].
func (r *Run) Replies() []int {
	var out []int
	for i, m := range r.Messages {
		if i > 0 && m.Role == "assistant" {
			out = append(out, i)
		}
	}
	return out
}

// CallID names the call answered by message i: the run and the call's number.
func (r *Run) CallID(seq int) string { return r.ID + "#" + strconv.Itoa(seq) }

// Request writes the body of a call as the agent sent it: the model, the messages before the reply
// and the tool list. Every piece is written as it came, so the body is the pieces and a few bytes
// of frame, which is what makes it possible to store the pieces once and rebuild the body exactly.
func Request(model string, msgs []string, tools string) string {
	var b strings.Builder
	b.WriteString(`{"model":`)
	b.WriteString(quote(model))
	b.WriteString(`,"messages":[`)
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(m)
	}
	b.WriteString(`],"tools":`)
	b.WriteString(tools)
	b.WriteByte('}')
	return b.String()
}

// Usage is what one call costs in tokens, as a provider reports it, split by where the input came from.
type Usage struct {
	Input, Cached, Output int64
	// Sources maps each source to its input tokens and the cached part of them.
	Sources map[string][2]int64
	TS      int64 // when the reply came back
	Sent    int64 // when the request was sent
}

// UsageOf works out the usage of the call answered by message i. The input is the tool list and
// every message before i. The provider serves from its cache the whole input of the run's previous
// call when that call was sent less than CacheWindow seconds before, as prompt caches do for a
// conversation that grows at its end.
func (r *Run) UsageOf(i int) Usage {
	u := Usage{Sources: map[string][2]int64{}, TS: r.Start + r.Messages[i].At, Sent: r.Start + r.Messages[i-1].At, Output: r.Messages[i].Tokens}
	prev := -1
	for j := i - 1; j > 0; j-- {
		if r.Messages[j].Role == "assistant" {
			prev = j
			break
		}
	}
	cachedUpTo := -1 // messages before this index are cached
	if prev > 0 && u.Sent-(r.Start+r.Messages[prev-1].At) <= CacheWindow {
		cachedUpTo = prev
	}
	add := func(src string, tokens int64, cached bool) {
		s := u.Sources[src]
		s[0] += tokens
		if cached {
			s[1] += tokens
		}
		u.Sources[src] = s
		u.Input += tokens
		if cached {
			u.Cached += tokens
		}
	}
	add("tools", r.ToolsTokens, cachedUpTo >= 0)
	for j := 0; j < i; j++ {
		add(r.Messages[j].Source, r.Messages[j].Tokens, j < cachedUpTo)
	}
	return u
}
