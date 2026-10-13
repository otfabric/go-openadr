// SPDX-License-Identifier: MIT

package timeline

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// The columns of the CSV form of a table.
var columns = []string{"interval", "sub", "start", "end", "type", "values", "randomize_start"}

// WriteCSV writes the table as CSV with a header line:
//
//	interval,sub,start,end,type,values,randomize_start
//	0,0,2030-01-01T00:00:00Z,2030-01-01T01:00:00Z,PRICE,0.17,
//
// Times are RFC 3339 and keep their offset; end is empty for a period
// without end. values is written as JSON: one value as it is (0.17, true,
// "text", {"x":1,"y":2}), several as an array. randomize_start is an ISO
// 8601 duration, or empty.
func (t Table) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(columns); err != nil {
		return err
	}
	for i, row := range t {
		values, err := encodeValues(row.Values)
		if err != nil {
			return fmt.Errorf("timeline: row %d: %w", i, err)
		}
		end, randomize := "", ""
		if !row.End.IsZero() {
			end = row.End.Format(time.RFC3339Nano)
		}
		if !row.RandomizeStart.IsZero() {
			randomize = row.RandomizeStart.String()
		}
		err = cw.Write([]string{
			strconv.FormatInt(int64(row.IntervalID), 10), strconv.Itoa(row.Sub),
			row.Start.Format(time.RFC3339Nano), end, row.Type, values, randomize,
		})
		if err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// ReadCSV reads a table that WriteCSV wrote, or one written by hand or by
// a spreadsheet. The header line is required and names the columns, in any
// order; start, type and values are required, and columns it does not know
// are ignored. A row without interval gets the number of its line, from 0.
// A cell of values that is not JSON is taken as a string.
func ReadCSV(r io.Reader) (Table, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("timeline: the CSV has no header line")
		}
		return nil, fmt.Errorf("timeline: %w", err)
	}
	at := map[string]int{}
	for i, name := range header {
		if i == 0 {
			// A byte order mark, as spreadsheets write it.
			name = strings.TrimPrefix(name, "\xef\xbb\xbf")
		}
		at[name] = i
	}
	for _, name := range []string{"start", "type", "values"} {
		if _, ok := at[name]; !ok {
			return nil, fmt.Errorf("timeline: the CSV has no column %q", name)
		}
	}
	var out Table
	for line := 0; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("timeline: %w", err)
		}
		cell := func(name string) string {
			if i, ok := at[name]; ok && i < len(rec) {
				return rec[i]
			}
			return ""
		}
		fail := func(name string, err error) (Table, error) {
			return nil, fmt.Errorf("timeline: row %d, column %s: %w", line, name, err)
		}
		row := Row{IntervalID: int32(line), Type: cell("type")}
		if row.Type == "" {
			return fail("type", errors.New("is empty"))
		}
		if v := cell("interval"); v != "" {
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return fail("interval", err)
			}
			row.IntervalID = int32(n)
		}
		if v := cell("sub"); v != "" {
			if row.Sub, err = strconv.Atoi(v); err != nil || row.Sub < 0 {
				return fail("sub", errors.New("is not a number from 0"))
			}
		}
		if row.Start, err = time.Parse(time.RFC3339Nano, cell("start")); err != nil {
			return fail("start", err)
		}
		if v := cell("end"); v != "" {
			if row.End, err = time.Parse(time.RFC3339Nano, v); err != nil {
				return fail("end", err)
			}
			if row.End.Before(row.Start) {
				return fail("end", errors.New("is before start"))
			}
		}
		if v := cell("randomize_start"); v != "" {
			if row.RandomizeStart, err = openadr.ParseDuration(v); err != nil {
				return fail("randomize_start", err)
			}
		}
		if row.Values, err = decodeValues(cell("values")); err != nil {
			return fail("values", err)
		}
		if len(out) >= maxRows {
			return nil, ErrTooLarge
		}
		out = append(out, row)
	}
}

type point struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

func encodeValue(v openadr.Value) (any, error) {
	switch v.Kind() {
	case openadr.KindNumber:
		n, _ := v.Number()
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, errors.New("a value is not a finite number")
		}
		return n, nil
	case openadr.KindString:
		s, _ := v.Text()
		return s, nil
	case openadr.KindBool:
		b, _ := v.Bool()
		return b, nil
	case openadr.KindPoint:
		p, _ := v.Point()
		return point{X: &p.X, Y: &p.Y}, nil
	default:
		return nil, errors.New("a value is not set")
	}
}

func encodeValues(values []openadr.Value) (string, error) {
	items := make([]any, len(values))
	for i, v := range values {
		var err error
		if items[i], err = encodeValue(v); err != nil {
			return "", err
		}
	}
	var out any = items
	if len(items) == 1 {
		out = items[0]
	}
	b, err := json.Marshal(out)
	return string(b), err
}

func decodeValue(raw json.RawMessage) (openadr.Value, error) {
	var (
		n  float64
		s  string
		b  bool
		pt point
	)
	switch {
	case strings.TrimSpace(string(raw)) == "null":
		// Which every type above would accept.
	case json.Unmarshal(raw, &n) == nil:
		return openadr.Number(n), nil
	case json.Unmarshal(raw, &b) == nil:
		return openadr.Bool(b), nil
	case json.Unmarshal(raw, &s) == nil:
		return openadr.String(s), nil
	case len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &pt) == nil && pt.X != nil && pt.Y != nil:
		return openadr.PointValue(*pt.X, *pt.Y), nil
	}
	return openadr.Value{}, fmt.Errorf("%s is not a number, a string, a boolean or a point", raw)
}

func decodeValues(cell string) ([]openadr.Value, error) {
	if cell == "" {
		return nil, errors.New("is empty")
	}
	raw := json.RawMessage(cell)
	if !json.Valid(raw) {
		// Text that was not quoted, as a person writes it.
		return []openadr.Value{openadr.String(cell)}, nil
	}
	var items []json.RawMessage
	if cell[0] != '[' {
		items = []json.RawMessage{raw}
	} else if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	out := make([]openadr.Value, len(items))
	for i, item := range items {
		var err error
		if out[i], err = decodeValue(item); err != nil {
			return nil, err
		}
	}
	return out, nil
}
