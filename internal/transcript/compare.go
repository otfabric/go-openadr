// SPDX-License-Identifier: MIT

package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// Diff kinds.
const (
	// DiffMissing: a member the recording has is absent.
	DiffMissing = "missing"
	// DiffExtra: a member the recording does not have is present.
	DiffExtra = "extra"
	// DiffType: the JSON types differ; null is a type of its own, so null
	// against a value, or against an absent member, is a difference too.
	DiffType = "type"
	// DiffValue: same type, different value.
	DiffValue = "value"
	// DiffLength: arrays of different lengths.
	DiffLength = "length"
	// DiffID: an ID that breaks the mapping of recorded IDs to replayed ones.
	DiffID = "id"
	// DiffSyntax: one side is not JSON.
	DiffSyntax = "syntax"
)

// Diff is one difference between a recorded JSON document (Want) and a
// replayed one (Got), at a JSON pointer.
type Diff struct {
	Path string
	Kind string
	Want string
	Got  string
}

func (d Diff) String() string {
	return fmt.Sprintf("%s %s: want %s, got %s", d.Path, d.Kind, d.Want, d.Got)
}

// Rules are the comparison rules for dynamic fields. The zero Rules compare
// everything exactly, numbers by value (1 and 1.0 are equal JSON numbers).
type Rules struct {
	// IDs maps recorded object IDs to replayed ones. When set, the members
	// that hold object IDs ("id", and names ending in "ID" but "clientID")
	// must map one to one: the first pair seen fixes the mapping.
	IDs *IDMap
	// Timestamps: createdDateTime and modificationDateTime must be RFC 3339
	// date-times on both sides; their values are not compared.
	Timestamps bool
	// Tokens: access_token must be a non-empty string on both sides; its
	// value is not compared. expires_in must be a number on both sides.
	Tokens bool
	// Ignore lists JSON pointers not compared at all. A trailing "/*"
	// ignores everything below a member.
	Ignore []string
	// IgnoreExtra accepts members the replay has and the recording has not.
	IgnoreExtra bool
	// Equivalent compares the date-times (createdDateTime,
	// modificationDateTime, start) as the instants they denote, and the
	// durations (duration, randomizeStart, timeZoneOffset) as the ISO 8601
	// durations they are: "+00:00" and "Z", ".840" and ".84", "PT1H" and
	// "P0Y0M0DT1H0M0S" are then equal. A value that does not parse is
	// compared as a string.
	Equivalent bool
}

var (
	dateTimeMembers = map[string]bool{"createdDateTime": true, "modificationDateTime": true, "start": true}
	durationMembers = map[string]bool{"duration": true, "randomizeStart": true, "timeZoneOffset": true}
)

// equivalent reports whether two strings of a member denote the same
// instant or the same duration.
func equivalent(member, a, b string) bool {
	switch {
	case dateTimeMembers[member]:
		ta, errA := time.Parse(time.RFC3339Nano, a)
		tb, errB := time.Parse(time.RFC3339Nano, b)
		return errA == nil && errB == nil && ta.Equal(tb)
	case durationMembers[member]:
		da, errA := openadr.ParseDuration(a)
		db, errB := openadr.ParseDuration(b)
		return errA == nil && errB == nil && da == db
	}
	return false
}

// IDMap is a one-to-one mapping of recorded to replayed IDs.
type IDMap struct {
	fwd, back map[string]string
}

// NewIDMap returns an empty mapping.
func NewIDMap() *IDMap { return &IDMap{fwd: map[string]string{}, back: map[string]string{}} }

// Match reports whether want and got may stand for the same object, and
// fixes the mapping when neither is mapped yet.
func (m *IDMap) Match(want, got string) bool {
	f, okf := m.fwd[want]
	b, okb := m.back[got]
	switch {
	case !okf && !okb:
		m.fwd[want], m.back[got] = got, want
		return true
	case okf && okb:
		return f == got && b == want
	}
	return false
}

// Rewrite replaces, in a string that is not itself an ID (an MQTT topic, a
// problem's detail), every recorded ID by its replayed one.
func (m *IDMap) Rewrite(s string) string {
	keys := make([]string, 0, len(m.fwd))
	for k := range m.fwd {
		if k != "" {
			keys = append(keys, k)
		}
	}
	// Longest first, so that an ID never replaces part of a longer one.
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		s = strings.ReplaceAll(s, k, m.fwd[k])
	}
	return s
}

// Map returns the replayed ID of a recorded one, or the recorded one when
// it has not been seen.
func (m *IDMap) Map(want string) string {
	if v, ok := m.fwd[want]; ok {
		return v
	}
	return want
}

// Compare compares a recorded JSON document with a replayed one.
func Compare(want, got []byte, r Rules) []Diff {
	w, err := decode(want)
	if err != nil {
		return []Diff{{Path: "", Kind: DiffSyntax, Want: err.Error(), Got: short(got)}}
	}
	g, err := decode(got)
	if err != nil {
		return []Diff{{Path: "", Kind: DiffSyntax, Want: short(want), Got: err.Error()}}
	}
	var out []Diff
	r.walk("", "", w, g, &out)
	return out
}

func decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("data after the JSON value")
	}
	return v, nil
}

func short(b []byte) string {
	if len(b) > 80 {
		return string(b[:80]) + "..."
	}
	return string(b)
}

func typeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func render(v any) string {
	b, _ := json.Marshal(v)
	return short(b)
}

func (r Rules) ignored(path string) bool {
	for _, p := range r.Ignore {
		if p == path || (strings.HasSuffix(p, "/*") && strings.HasPrefix(path, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

// isIDMember reports whether a member holds object IDs.
func isIDMember(name string) bool {
	return name == "id" || (strings.HasSuffix(name, "ID") && name != "clientID") || strings.HasSuffix(name, "IDs")
}

func escape(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

func (r Rules) walk(path, member string, w, g any, out *[]Diff) {
	if r.ignored(path) {
		return
	}
	if typeOf(w) != typeOf(g) {
		*out = append(*out, Diff{Path: path, Kind: DiffType, Want: render(w), Got: render(g)})
		return
	}
	switch w := w.(type) {
	case map[string]any:
		gm := g.(map[string]any)
		keys := make([]string, 0, len(w)+len(gm))
		for k := range w {
			keys = append(keys, k)
		}
		for k := range gm {
			if _, ok := w[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := path + "/" + escape(k)
			wv, inW := w[k]
			gv, inG := gm[k]
			switch {
			case r.ignored(p):
			case !inG:
				*out = append(*out, Diff{Path: p, Kind: DiffMissing, Want: render(wv), Got: "(absent)"})
			case !inW:
				if !r.IgnoreExtra {
					*out = append(*out, Diff{Path: p, Kind: DiffExtra, Want: "(absent)", Got: render(gv)})
				}
			default:
				r.walk(p, k, wv, gv, out)
			}
		}
	case []any:
		ga := g.([]any)
		if len(w) != len(ga) {
			*out = append(*out, Diff{Path: path, Kind: DiffLength, Want: fmt.Sprint(len(w)), Got: fmt.Sprint(len(ga))})
		}
		for i := 0; i < len(w) && i < len(ga); i++ {
			r.walk(fmt.Sprintf("%s/%d", path, i), member, w[i], ga[i], out)
		}
	case json.Number:
		a, _, errA := big.ParseFloat(w.String(), 10, 256, big.ToNearestEven)
		b, _, errB := big.ParseFloat(g.(json.Number).String(), 10, 256, big.ToNearestEven)
		if (r.Tokens && member == "expires_in") || (errA == nil && errB == nil && a.Cmp(b) == 0) {
			return
		}
		*out = append(*out, Diff{Path: path, Kind: DiffValue, Want: w.String(), Got: g.(json.Number).String()})
	case string:
		gs := g.(string)
		switch {
		case r.Timestamps && (member == "createdDateTime" || member == "modificationDateTime"):
			if !isTime(w) || !isTime(gs) {
				*out = append(*out, Diff{Path: path, Kind: DiffValue, Want: w, Got: gs})
			}
		case r.Tokens && (member == "access_token" || member == "refresh_token"):
			if w == "" || gs == "" {
				*out = append(*out, Diff{Path: path, Kind: DiffValue, Want: w, Got: gs})
			}
		case r.IDs != nil && isIDMember(member):
			if !r.IDs.Match(w, gs) {
				*out = append(*out, Diff{Path: path, Kind: DiffID, Want: w, Got: gs})
			}
		case w != gs && (!r.Equivalent || !equivalent(member, w, gs)) && (r.IDs == nil || r.IDs.Rewrite(w) != gs):
			*out = append(*out, Diff{Path: path, Kind: DiffValue, Want: w, Got: gs})
		}
	default:
		if w != g {
			*out = append(*out, Diff{Path: path, Kind: DiffValue, Want: render(w), Got: render(g)})
		}
	}
}

func isTime(s string) bool {
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}
