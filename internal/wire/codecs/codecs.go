// SPDX-License-Identifier: MIT

// Package codecs returns the codec of a protocol revision.
package codecs

import (
	"fmt"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
	v301 "github.com/otfabric/go-openadr/internal/wire/v301"
	v310 "github.com/otfabric/go-openadr/internal/wire/v310"
)

// For returns the codec of revision v; the zero Version is 3.1.0.
func For(v openadr.Version) (*wire.Codec, error) {
	switch v {
	case "", openadr.Version310:
		return v310.Codec(), nil
	case openadr.Version301:
		return v301.Codec(), nil
	default:
		return nil, fmt.Errorf("OpenADR %q is not a revision this module speaks (%s or %s)", string(v), openadr.Version310, openadr.Version301)
	}
}
