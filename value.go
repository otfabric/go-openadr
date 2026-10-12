// SPDX-License-Identifier: MIT

package openadr

import "strconv"

// ValueKind is the JSON type of a Value.
type ValueKind uint8

// Kinds of value a ValuesMap may hold.
const (
	KindNumber ValueKind = iota + 1
	KindString
	KindBool
	KindPoint
)

func (k ValueKind) String() string {
	switch k {
	case KindNumber:
		return "number"
	case KindString:
		return "string"
	case KindBool:
		return "bool"
	case KindPoint:
		return "point"
	default:
		return "invalid"
	}
}

// Point is a pair of numbers, typically a point on a two dimensional grid.
type Point struct {
	X, Y float64
}

// Value is one data point of a ValuesMap: a number, a string, a boolean or
// a Point. The zero Value is invalid. Values are comparable with ==.
//
// JSON does not distinguish integers from other numbers, so both are held as
// a float64; integers beyond 2^53 lose precision.
type Value struct {
	kind ValueKind
	num  float64
	str  string
	b    bool
	pt   Point
}

// Number returns a numeric Value.
func Number(n float64) Value { return Value{kind: KindNumber, num: n} }

// String returns a string Value.
func String(s string) Value { return Value{kind: KindString, str: s} }

// Bool returns a boolean Value.
func Bool(b bool) Value { return Value{kind: KindBool, b: b} }

// PointValue returns a Value holding the point (x, y).
func PointValue(x, y float64) Value { return Value{kind: KindPoint, pt: Point{X: x, Y: y}} }

// Kind reports what the Value holds; it is 0 for the zero Value.
func (v Value) Kind() ValueKind { return v.kind }

// Number returns the number held and whether the Value is a number.
func (v Value) Number() (float64, bool) { return v.num, v.kind == KindNumber }

// Text returns the string held and whether the Value is a string.
func (v Value) Text() (string, bool) { return v.str, v.kind == KindString }

// Bool returns the boolean held and whether the Value is a boolean.
func (v Value) Bool() (bool, bool) { return v.b, v.kind == KindBool }

// Point returns the point held and whether the Value is a point.
func (v Value) Point() (Point, bool) { return v.pt, v.kind == KindPoint }

// String formats the Value for diagnostics.
func (v Value) String() string {
	switch v.kind {
	case KindNumber:
		return strconv.FormatFloat(v.num, 'f', -1, 64)
	case KindString:
		return strconv.Quote(v.str)
	case KindBool:
		return strconv.FormatBool(v.b)
	case KindPoint:
		return "(" + strconv.FormatFloat(v.pt.X, 'f', -1, 64) + "," + strconv.FormatFloat(v.pt.Y, 'f', -1, 64) + ")"
	default:
		return "<invalid>"
	}
}
