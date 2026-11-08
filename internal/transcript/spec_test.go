// SPDX-License-Identifier: MIT

package transcript_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/transcript"
	"github.com/otfabric/go-openadr/internal/wire/codecs"
)

// The cases of the specification (testdata/transcripts/spec): an instance
// of each component schema of the OpenAPI document of a revision, made of
// the examples, defaults and first enum values the document gives
// (scripts/transcripts-spec-cases.py). Every instance of a schema this
// module has a codec for is decoded and encoded again; the two must agree
// but for the differences the table of transcript.Known classifies under
// the source "spec-<revision>".

type specCases struct {
	Version string `json:"version"`
	Cases   []struct {
		Schema   string          `json:"schema"`
		Variant  string          `json:"variant"`
		Instance json.RawMessage `json:"instance"`
		Missing  []string        `json:"missing"`
	} `json:"cases"`
}

// specShapes maps the schemas with a codec to the shape of their bodies.
var specShapes = map[string]transcript.Shape{
	"program":                {Kind: transcript.ShapeProgram},
	"programRequest":         {Kind: transcript.ShapeProgram, Request: true},
	"event":                  {Kind: transcript.ShapeEvent},
	"eventRequest":           {Kind: transcript.ShapeEvent, Request: true},
	"report":                 {Kind: transcript.ShapeReport},
	"reportRequest":          {Kind: transcript.ShapeReport, Request: true},
	"subscription":           {Kind: transcript.ShapeSubscription},
	"subscriptionRequest":    {Kind: transcript.ShapeSubscription, Request: true},
	"ven":                    {Kind: transcript.ShapeVEN},
	"venRequest":             {Kind: transcript.ShapeVEN, Request: true},
	"BlVenRequest":           {Kind: transcript.ShapeVEN, Request: true},
	"VenVenRequest":          {Kind: transcript.ShapeVEN, Request: true},
	"resource":               {Kind: transcript.ShapeResource},
	"resourceRequest":        {Kind: transcript.ShapeResource, Request: true},
	"BlResourceRequest":      {Kind: transcript.ShapeResource, Request: true},
	"VenResourceRequest":     {Kind: transcript.ShapeResource, Request: true},
	"notification":           {Kind: transcript.ShapeNotification},
	"notifiersResponse":      {Kind: transcript.ShapeNotifiers},
	"notifierTopicsResponse": {Kind: transcript.ShapeTopics},
	"problem":                {Kind: transcript.ShapeProblem},
	"authServerInfo":         {Kind: transcript.ShapeAuthServer},
}

// specValues are the schemas of single values, checked with the parsers
// of the module.
var specValues = map[string]func(string) error{
	"duration": func(s string) error { _, err := openadr.ParseDuration(s); return err },
	"dateTime": func(s string) error { _, err := time.Parse(time.RFC3339, s); return err },
	"objectID": func(s string) error {
		if !openadr.ValidID(s) {
			return errors.New("not an object ID")
		}
		return nil
	},
}

func TestSpecCases(t *testing.T) {
	for _, version := range []string{"3.1.0", "3.0.1"} {
		t.Run(version, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(corpus, "spec", version, "cases.json"))
			if err != nil {
				t.Fatal(err)
			}
			var cs specCases
			if err := json.Unmarshal(data, &cs); err != nil {
				t.Fatal(err)
			}
			c, err := codecs.For(openadr.Version(version))
			if err != nil {
				t.Fatal(err)
			}
			var decoded, values, other int
			for _, k := range cs.Cases {
				where := k.Schema + " (" + k.Variant + ")"
				if check, ok := specValues[k.Schema]; ok {
					var s string
					if err := json.Unmarshal(k.Instance, &s); err != nil {
						t.Errorf("%s: %v", where, err)
					} else if err := check(s); err != nil {
						t.Errorf("%s: the example %q is refused: %v", where, s, err)
					}
					values++
					continue
				}
				sh, ok := specShapes[k.Schema]
				if !ok {
					other++
					continue
				}
				out, err := transcript.Reencode(c, sh, k.Instance)
				if err != nil {
					if cls, ok := transcript.KnownRefusal("spec-"+version, k.Schema, err); ok {
						t.Logf("%s: refused as classified (%s)", where, cls)
						continue
					}
					t.Errorf("%s: %v\n%s", where, err, k.Instance)
					continue
				}
				decoded++
				for _, d := range transcript.Compare(k.Instance, out, transcript.Rules{Equivalent: true}) {
					if _, ok := transcript.Known("spec-"+version, sh.Kind, d); !ok {
						t.Errorf("%s: unclassified difference %s", where, d)
					}
				}
			}
			t.Logf("%d instances decoded and encoded again, %d values parsed, %d schemas without a codec of their own (parts of the objects)", decoded, values, other)
			if decoded == 0 {
				t.Fatal("no case decoded")
			}
			_ = strings.TrimSpace
		})
	}
}
