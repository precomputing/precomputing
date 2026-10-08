package engine

import (
	"math"
	"math/bits"
	"strconv"
	"strings"

	"precomputing.com/precomputing/policy"
)

// num is a number as SQLite holds it while evaluating: an integer or a real.
// Derived values follow SQLite's rules, so integer inputs stay exact integers
// until a division or an overflow turns them into reals.
type num struct {
	isInt bool
	i     int64
	f     float64
}

func (n num) float() float64 {
	if n.isInt {
		return float64(n.i)
	}
	return n.f
}

// expr evaluates a derived value. ok is false where SQL would give NULL (division by zero).
type expr func(v []num) (r num, ok bool)

func compileExpr(e policy.Expr, index map[string]int) expr {
	switch x := e.(type) {
	case policy.Num:
		n := parseNum(x.Text)
		return func([]num) (num, bool) { return n, true }
	case policy.Ref:
		i := index[x.Name]
		return func(v []num) (num, bool) { return v[i], true }
	case policy.Neg:
		in := compileExpr(x.X, index)
		return func(v []num) (num, bool) {
			a, ok := in(v)
			if !ok {
				return a, false
			}
			if a.isInt {
				if a.i == math.MinInt64 {
					return num{f: -float64(a.i)}, true
				}
				return num{isInt: true, i: -a.i}, true
			}
			return num{f: -a.f}, true
		}
	case policy.Binary:
		l, r := compileExpr(x.L, index), compileExpr(x.R, index)
		op := x.Op
		return func(v []num) (num, bool) {
			a, ok := l(v)
			if !ok {
				return a, false
			}
			b, ok := r(v)
			if !ok {
				return b, false
			}
			return arith(op, a, b)
		}
	}
	return func([]num) (num, bool) { return num{}, false }
}

// arith applies one operator the way SQLite does. The compiler writes a / b as 1.0 * a / b,
// so division is always between reals, and division by zero is NULL.
func arith(op byte, a, b num) (num, bool) {
	if op == '/' {
		d := b.float()
		if d == 0 {
			return num{}, false
		}
		return num{f: (1.0 * a.float()) / d}, true
	}
	if a.isInt && b.isInt {
		var r int64
		var overflow bool
		switch op {
		case '+':
			r = a.i + b.i
			overflow = (a.i > 0 && b.i > 0 && r < 0) || (a.i < 0 && b.i < 0 && r >= 0)
		case '-':
			r = a.i - b.i
			overflow = (a.i >= 0 && b.i < 0 && r < 0) || (a.i < 0 && b.i > 0 && r >= 0)
		case '*':
			if a.i != 0 && b.i != 0 {
				hi, lo := mul128(a.i, b.i)
				r = lo
				overflow = !(hi == 0 && lo >= 0 || hi == -1 && lo < 0)
			}
		}
		if !overflow {
			return num{isInt: true, i: r}, true
		}
	}
	x, y := a.float(), b.float()
	switch op {
	case '+':
		return num{f: x + y}, true
	case '-':
		return num{f: x - y}, true
	}
	return num{f: float64(x * y)}, true
}

// mul128 returns the 128-bit product of a and b as a high and a low word.
func mul128(a, b int64) (hi, lo int64) {
	neg := (a < 0) != (b < 0)
	ua, ub := uint64(a), uint64(b)
	if a < 0 {
		ua = uint64(-a)
	}
	if b < 0 {
		ub = uint64(-b)
	}
	h, l := bits.Mul64(ua, ub)
	if neg {
		l = ^l + 1
		h = ^h
		if l == 0 {
			h++
		}
	}
	return int64(h), int64(l)
}

// parseNum reads a number literal as SQLite does: integers stay integers unless they are too large.
func parseNum(text string) num {
	if !strings.ContainsAny(text, ".eE") {
		if i, err := strconv.ParseInt(text, 10, 64); err == nil {
			return num{isInt: true, i: i}
		}
	}
	f, _ := strconv.ParseFloat(text, 64)
	return num{f: f}
}
