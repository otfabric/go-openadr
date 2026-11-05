// SPDX-License-Identifier: MIT

package codecs

import (
	"testing"

	openadr "github.com/otfabric/go-openadr"
)

func TestFor(t *testing.T) {
	for v, want := range map[openadr.Version]openadr.Version{
		"":                 openadr.Version310,
		openadr.Version310: openadr.Version310,
		openadr.Version301: openadr.Version301,
	} {
		if c, err := For(v); err != nil || c.Version != want {
			t.Errorf("For(%q) = %v, %v; want %s", v, c, err, want)
		}
	}
	for _, v := range []openadr.Version{"3.0.0", "3.1", "3.2.0", "v3.1.0"} {
		if c, err := For(v); err == nil {
			t.Errorf("For(%q) = %v, want an error", v, c)
		}
	}
}
