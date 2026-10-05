package mcp

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Everything a tool returns is text written for a model to read: a line or two that says what
// the rows are, then the rows as CSV with a header. The same code runs natively and in the
// browser, and the text is the same to the byte in both, so numbers are formatted here and not
// by the platform.

// num formats a number: whole numbers as integers, others to ten significant digits, never
// with an exponent.
func num(x float64) string { return digits(x, 10) }

// digits formats a number to sig significant digits, never with an exponent; whole numbers
// that fit are written in full.
func digits(x float64, sig int) string {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return ""
	}
	if x == math.Trunc(x) && math.Abs(x) < 1e15 && (sig >= 7 || math.Abs(x) < math.Pow(10, float64(sig))) {
		return strconv.FormatInt(int64(x), 10)
	}
	s := strconv.FormatFloat(x, 'e', sig-1, 64) // d.ddde±XX, correctly rounded on every platform
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	at := strings.IndexByte(s, 'e')
	digits := s[:1] + s[2:at]
	exp, _ := strconv.Atoi(s[at+1:])
	point := exp + 1 // where the decimal point falls in digits
	var out string
	switch {
	case point <= 0:
		out = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		out = digits + strings.Repeat("0", point-len(digits))
	default:
		out = digits[:point] + "." + digits[point:]
	}
	if strings.Contains(out, ".") {
		out = strings.TrimRight(out, "0")
		out = strings.TrimSuffix(out, ".")
	}
	if neg && out != "0" {
		out = "-" + out
	}
	return out
}

// cell formats one value read from the file.
func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return num(x)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// asInt reads a whole number, as the native build (int64) or the browser build (float64) gives it.
func asInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x), true
		}
	}
	return 0, false
}

// asFloat reads a number.
func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// when formats Unix seconds as an RFC 3339 time in UTC.
func when(sec int64) string {
	return time.Unix(sec, 0).UTC().Format("2006-01-02T15:04:05Z")
}

// whenCell formats a time read from the file.
func whenCell(v any) string {
	if n, ok := asInt(v); ok {
		return when(n)
	}
	return cell(v)
}

// table writes rows as CSV with a header line (RFC 4180 quoting).
type table struct {
	b    strings.Builder
	rows int
}

func (t *table) line(fields ...string) {
	for i, f := range fields {
		if i > 0 {
			t.b.WriteByte(',')
		}
		if f != "" && (strings.ContainsAny(f, ",\"\n\r") || f[0] == ' ' || f[len(f)-1] == ' ') {
			t.b.WriteByte('"')
			t.b.WriteString(strings.ReplaceAll(f, `"`, `""`))
			t.b.WriteByte('"')
		} else {
			t.b.WriteString(f)
		}
	}
	t.b.WriteByte('\n')
}

func (t *table) row(fields ...string) {
	t.line(fields...)
	t.rows++
}

func (t *table) String() string { return t.b.String() }

// durationText writes seconds the way policies write durations: 10s, 5m, 1h, 30d, 1y.
func durationText(sec int64) string {
	switch {
	case sec < 0:
		return "forever"
	case sec == 0:
		return "0s"
	case sec%31536000 == 0:
		return fmt.Sprintf("%dy", sec/31536000)
	case sec%604800 == 0 && sec/604800 < 10:
		return fmt.Sprintf("%dw", sec/604800)
	case sec%86400 == 0:
		return fmt.Sprintf("%dd", sec/86400)
	case sec%3600 == 0:
		return fmt.Sprintf("%dh", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%ds", sec)
}

// parseDuration reads a duration such as 10s, 5m, 1h, 1d or 1w.
func parseDuration(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) < 2 {
		return 0, fmt.Errorf("%q is not a duration such as 10s, 1m, 1h or 1d", s)
	}
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a duration such as 10s, 1m, 1h or 1d", s)
	}
	unit := map[byte]int64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400, 'w': 604800, 'y': 31536000}[s[len(s)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("%q is not a duration such as 10s, 1m, 1h or 1d", s)
	}
	return n * unit, nil
}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseTime reads a time given as Unix seconds or as an RFC 3339 time. A time without an offset
// is UTC.
func parseTime(v any) (int64, error) {
	switch x := v.(type) {
	case float64:
		if x != math.Trunc(x) || math.Abs(x) > 1e12 {
			return 0, fmt.Errorf("%v is not a time in whole Unix seconds", x)
		}
		return int64(x), nil
	case string:
		s := strings.TrimSpace(x)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		for _, l := range timeLayouts {
			if t, err := time.Parse(l, s); err == nil {
				return t.Unix(), nil
			}
		}
		return 0, fmt.Errorf("%q is not a time; use RFC 3339, such as 2026-09-28T10:00:00Z or 2026-09-28T10:30:00-04:00, or Unix seconds", x)
	}
	return 0, errors.New("a time is a string such as 2026-09-28T10:00:00Z, or Unix seconds")
}

// plural writes a count with its noun.
func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(n, 10) + " " + many
}
