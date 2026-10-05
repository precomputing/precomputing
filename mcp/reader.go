package mcp

import (
	"errors"
	"strings"
)

// Reader runs read-only SQL against one Precomputing file. The native build reads the file
// through a read-only SQLite connection (SQLiteReader); the browser build reads the page's
// SQLite WebAssembly database. Values are nil, int64, float64 or string; the browser gives
// every number as a float64, and the text the tools write is the same either way.
type Reader interface {
	// Read runs one statement and returns its columns and at most max rows (all rows when max is
	// 0), with More set when there were more. A statement that would change the file is refused.
	Read(sql string, args []any, max int) (*Rows, error)
}

// Rows is the result of one read.
type Rows struct {
	Columns []string
	Rows    [][]any
	More    bool
}

// SingleStatement refuses SQL with more than one statement in it; readers call it before they
// run anything.
func SingleStatement(sql string) error { return singleStatement(sql) }

// singleStatement refuses SQL with more than one statement in it. Semicolons inside strings,
// quoted names and comments do not count; trailing semicolons and comments are allowed. The
// native reader checks again with SQLite itself.
func singleStatement(sql string) error {
	s := sql
	ended := false // a semicolon ended the first statement
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`' || c == '[':
			end := c
			if c == '[' {
				end = ']'
			}
			if ended {
				return errors.New("one statement at a time, please")
			}
			j := i + 1
			for j < len(s) {
				if s[j] == end {
					if end != ']' && j+1 < len(s) && s[j+1] == end { // a doubled quote
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return nil
			}
			i += j
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return nil
			}
			i += j + 3
		case c == ';':
			ended = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
		default:
			if ended {
				return errors.New("one statement at a time, please")
			}
		}
	}
	if strings.TrimSpace(strings.Trim(sql, "; \t\r\n")) == "" {
		return errors.New("there is no statement to run")
	}
	return nil
}
