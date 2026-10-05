// Package policy reads .precompute files: the declarations that say which
// streams exist, which answers to keep ready and how long each level of
// detail survives.
package policy

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind is the type of a token.
type Kind int

const (
	EOF Kind = iota
	Ident
	Int      // 20
	Number   // 0.5, 4
	Duration // 10s, 1m, 24h, 30d, 2w, 1y
	Percent  // 1%
	LBrace
	RBrace
	LParen
	RParen
	Comma
	Dot
	Assign
	Greater
	Plus
	Minus
	Star
	Slash
	String // "text in quotes"
)

var kindNames = map[Kind]string{
	EOF: "end of file", Ident: "name", Int: "whole number", Number: "number",
	Duration: "duration", Percent: "percentage", LBrace: "'{'", RBrace: "'}'",
	LParen: "'('", RParen: "')'", Comma: "','", Dot: "'.'", Assign: "'='",
	Greater: "'>'", Plus: "'+'", Minus: "'-'", Star: "'*'", Slash: "'/'", String: "text in quotes",
}

func (k Kind) String() string { return kindNames[k] }

// Pos is a place in the source, counted from 1.
type Pos struct{ Line, Col int }

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// Token is one word or symbol of a policy file.
type Token struct {
	Kind Kind
	Text string
	Pos  Pos
	Num  float64 // Int, Number, Percent (as a fraction: 1% = 0.01)
	Secs int64   // Duration, in seconds
}

// Error is a problem found in a policy file, with its place.
type Error struct {
	Pos Pos
	Msg string
}

func (e *Error) Error() string { return e.Pos.String() + ": " + e.Msg }

var unitSeconds = map[byte]int64{
	's': 1, 'm': 60, 'h': 3600, 'd': 86400, 'w': 7 * 86400, 'y': 365 * 86400,
}

func isLetter(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }

// Lex splits a policy into tokens. Comments run from '#' to the end of the line. Text in double
// quotes is one token; inside it \" is a quote and every other character, backslashes included,
// stands for itself, so regular expressions need no extra escaping.
func Lex(src string) ([]Token, error) {
	var toks []Token
	line, col := 1, 1
	i := 0
	advance := func(n int) {
		for k := 0; k < n; k++ {
			if src[i] == '\n' {
				line++
				col = 1
			} else {
				col++
			}
			i++
		}
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n' || c == ' ' || c == '\t' || c == '\r':
			advance(1)
			continue
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				advance(1)
			}
			continue
		}
		pos := Pos{line, col}
		switch {
		case c == '"':
			var b strings.Builder
			j := i + 1
			for ; j < len(src) && src[j] != '"'; j++ {
				if src[j] == '\n' {
					return nil, &Error{pos, "text in quotes must end on the line it starts"}
				}
				if src[j] == '\\' && j+1 < len(src) && src[j+1] == '"' {
					j++
				}
				b.WriteByte(src[j])
			}
			if j >= len(src) {
				return nil, &Error{pos, "text in quotes is not closed"}
			}
			toks = append(toks, Token{Kind: String, Text: b.String(), Pos: pos})
			advance(j + 1 - i)
		case isLetter(c):
			j := i
			for j < len(src) && (isLetter(src[j]) || isDigit(src[j])) {
				j++
			}
			toks = append(toks, Token{Kind: Ident, Text: src[i:j], Pos: pos})
			advance(j - i)
		case isDigit(c):
			j := i
			for j < len(src) && isDigit(src[j]) {
				j++
			}
			isFloat := false
			if j+1 < len(src) && src[j] == '.' && isDigit(src[j+1]) {
				isFloat = true
				j++
				for j < len(src) && isDigit(src[j]) {
					j++
				}
			}
			text := src[i:j]
			v, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, &Error{pos, fmt.Sprintf("cannot read the number %q", text)}
			}
			switch {
			case j < len(src) && src[j] == '%':
				toks = append(toks, Token{Kind: Percent, Text: text + "%", Pos: pos, Num: v / 100})
				j++
			case j < len(src) && !isFloat && unitSeconds[src[j]] != 0 && (j+1 >= len(src) || !(isLetter(src[j+1]) || isDigit(src[j+1]))):
				n, _ := strconv.ParseInt(text, 10, 64)
				toks = append(toks, Token{Kind: Duration, Text: src[i : j+1], Pos: pos, Secs: n * unitSeconds[src[j]]})
				j++
			case j < len(src) && (isLetter(src[j])):
				k := j
				for k < len(src) && (isLetter(src[k]) || isDigit(src[k])) {
					k++
				}
				return nil, &Error{pos, fmt.Sprintf("%q is not a number or a duration; durations end in s, m, h, d, w or y, as in 10s or 30d", src[i:k])}
			case isFloat:
				toks = append(toks, Token{Kind: Number, Text: text, Pos: pos, Num: v})
			default:
				toks = append(toks, Token{Kind: Int, Text: text, Pos: pos, Num: v})
			}
			advance(j - i)
		default:
			k, ok := map[byte]Kind{'{': LBrace, '}': RBrace, '(': LParen, ')': RParen, ',': Comma,
				'.': Dot, '=': Assign, '>': Greater, '+': Plus, '-': Minus, '*': Star, '/': Slash}[c]
			if !ok {
				return nil, &Error{pos, fmt.Sprintf("unexpected character %q", string(c))}
			}
			toks = append(toks, Token{Kind: k, Text: string(c), Pos: pos})
			advance(1)
		}
	}
	toks = append(toks, Token{Kind: EOF, Pos: Pos{line, col}})
	return toks, nil
}

// FormatDuration writes seconds the way a policy would: 90 -> "90s", 3600 -> "1h".
func FormatDuration(secs int64) string {
	if secs < 0 {
		return "forever"
	}
	for _, u := range []struct {
		n int64
		s string
	}{{365 * 86400, "y"}, {7 * 86400, "w"}, {86400, "d"}, {3600, "h"}, {60, "m"}} {
		if secs >= u.n && secs%u.n == 0 {
			return strconv.FormatInt(secs/u.n, 10) + u.s
		}
	}
	return strconv.FormatInt(secs, 10) + "s"
}
