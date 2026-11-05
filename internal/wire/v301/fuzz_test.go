// SPDX-License-Identifier: MIT

package v301

import (
	"bytes"
	"errors"
	"testing"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
)

// fuzzObject checks the invariants of one object codec on untrusted input:
// decoding never panics; a fault is a *openadr.ValidationError; what decodes
// and validates encodes in the form it was read in, decodes again to that
// form, and encodes to the same bytes.
func fuzzObject[T any](t *testing.T, c wire.ObjectCodec[T], data []byte, validate func(T) error) {
	t.Helper()
	v, form, err := c.Unmarshal(data)
	if err != nil {
		var ve *openadr.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("Unmarshal(%q): %v is not a ValidationError", data, err)
		}
		return
	}
	if validate(v) != nil {
		return
	}
	out, err := c.Marshal(v, form)
	if err != nil {
		// 3.0.1 refuses what the domain model allows and it cannot carry
		// (a report without programID, say): refused, never dropped.
		var ve *openadr.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("Marshal of a decoded %T in form %v: %v", v, form, err)
		}
		return
	}
	again, form2, err := c.Unmarshal(out)
	if err != nil {
		t.Fatalf("re-decoding %s: %v", out, err)
	}
	if form2 != form {
		t.Fatalf("form %v became %v through %s", form, form2, out)
	}
	out2, err := c.Marshal(again, form)
	if err != nil || !bytes.Equal(out, out2) {
		t.Fatalf("encoding is not stable (%v):\n%s\n%s", err, out, out2)
	}
	// A list of what decoded decodes too.
	if _, err := c.UnmarshalList([]byte("[" + string(out) + "," + string(out) + "]")); err != nil {
		t.Fatalf("UnmarshalList of [%s,…]: %v", out, err)
	}
}

// fuzzOther checks a decoder of a non-object message: no panic, and what
// decodes encodes to bytes that decode and encode the same.
func fuzzOther[T any](t *testing.T, data []byte, unmarshal func([]byte) (T, error), marshal func(T) ([]byte, error)) {
	t.Helper()
	v, err := unmarshal(data)
	if err != nil {
		return
	}
	out, err := marshal(v)
	if err != nil {
		return // e.g. a notification whose object does not validate
	}
	again, err := unmarshal(out)
	if err != nil {
		t.Fatalf("re-decoding %s: %v", out, err)
	}
	out2, err := marshal(again)
	if err != nil || !bytes.Equal(out, out2) {
		t.Fatalf("encoding is not stable (%v):\n%s\n%s", err, out, out2)
	}
}

var fuzzSeeds = []string{
	`{"programName":"p","programLongName":"l","retailerName":"r","retailerLongName":"rl","programType":"t","country":"US","principalSubdivision":"CO","timeZoneOffset":"PT1H","bindingEvents":true,"localPrice":false,"intervalPeriod":{"start":"2024-07-25T08:31:10Z","duration":"PT1H"},"programDescriptions":[{"URL":"https://x"}],"payloadDescriptors":[{"objectType":"EVENT_PAYLOAD_DESCRIPTOR","payloadType":"PRICE"}],"targets":[{"type":"GROUP","values":["g"]}]}`,
	`{"id":"e","createdDateTime":"2024-07-25T08:31:10Z","objectType":"EVENT","programID":"p","priority":1,"reportDescriptors":[{"payloadType":"USAGE","targets":[{"type":"GROUP","values":["g"]}]}],"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[0.17,"a",true,{"x":1,"y":2}]}]}]}`,
	`{"programID":"p","eventID":"e","clientName":"c","payloadDescriptors":[{"objectType":"REPORT_PAYLOAD_DESCRIPTOR","payloadType":"USAGE"}],"resources":[{"resourceName":"r","intervals":[{"id":-1,"payloads":[]}]}]}`,
	`{"venName":"v","attributes":[{"type":"LOCATION","values":[{"x":1,"y":2}]}],"targets":[{"type":"GROUP","values":["g"]}]}`,
	`{"resourceName":"r","venID":"v"}`,
	`{"clientName":"c","programID":"p","objectOperations":[{"objects":["EVENT"],"operations":["POST"],"callbackUrl":"https://x","bearerToken":"t"}]}`,
	`{"objectType":"EVENT","operation":"POST","object":{"programID":"p","intervals":[]},"targets":[{"type":"GROUP","values":["g"]}]}`,
	`{"objectType":"PROGRAM","operation":"DELETE","object":{"programName":"p","timeZoneOffset":"x"}}`,
	`{"type":"about:blank","title":"t","status":400,"detail":"d"}`,
	`[{"programID":"p","intervals":[]}]`,
	`{"intervals":[{"id":2147483648,"payloads":[]}]}`,
	`null`, ``, `{`, `[]`,
}

// FuzzCodec feeds untrusted bytes to every decoder of the 3.0.1 codec; kind
// selects which.
func FuzzCodec(f *testing.F) {
	for _, s := range fuzzSeeds {
		for kind := range uint8(9) {
			f.Add(kind, []byte(s))
		}
	}
	c := Codec()
	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		switch kind % 9 {
		case 0:
			fuzzObject(t, c.Programs, data, openadr.Program.Validate)
		case 1:
			fuzzObject(t, c.Events, data, openadr.Event.Validate)
		case 2:
			fuzzObject(t, c.Reports, data, openadr.Report.Validate)
		case 3:
			fuzzObject(t, c.VENs, data, openadr.VEN.Validate)
		case 4:
			fuzzObject(t, c.Resources, data, openadr.Resource.Validate)
		case 5:
			fuzzObject(t, c.Subscriptions, data, openadr.Subscription.Validate)
		case 6:
			fuzzOther(t, data, c.UnmarshalNotification, c.MarshalNotification)
		case 7:
			fuzzOther(t, data, c.UnmarshalProblem, c.MarshalProblem)
		case 8:
			// A list fails as a whole or decodes every item.
			if vs, err := c.Programs.UnmarshalList(data); err == nil {
				if _, err := c.Programs.MarshalList(vs); err != nil {
					var ve *openadr.ValidationError
					if !errors.As(err, &ve) {
						t.Fatalf("MarshalList of decoded programs: %v", err)
					}
				}
			}
		}
	})
}
