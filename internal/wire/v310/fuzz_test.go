// SPDX-License-Identifier: MIT

package v310

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
		t.Fatalf("Marshal of a decoded %T in form %v: %v", v, form, err)
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
	`{"programName":"p","intervalPeriod":{"start":"2023-06-15T09:30:00Z","duration":"P9999Y"},"payloadDescriptors":[{"objectType":"EVENT_PAYLOAD_DESCRIPTOR","payloadType":"PRICE"},{"objectType":"REPORT_PAYLOAD_DESCRIPTOR","payloadType":"USAGE","accuracy":0.5,"confidence":90}],"attributes":[{"type":"X","values":[1]}],"targets":["a"]}`,
	`{"id":"e","createdDateTime":"2023-06-15T09:30:00Z","objectType":"EVENT","programID":"p","intervals":[{"id":0,"intervalPeriod":{"start":"0001-01-01"},"payloads":[{"type":"PRICE","values":[0.17,"a",true,{"x":1,"y":2}]}]}]}`,
	`{"programID":"p","eventID":"e","clientName":"c","resources":[{"resourceName":"r","intervals":[{"id":-1,"payloads":[]}]}]}`,
	`{"objectType":"BL_VEN_REQUEST","venName":"v","clientID":"c","targets":["t"],"attributes":[{"type":"LOCATION","values":[{"x":1,"y":2}]}]}`,
	`{"objectType":"VEN_VEN_REQUEST","venName":"v"}`,
	`{"objectType":"BL_RESOURCE_REQUEST","resourceName":"r","venID":"v","clientID":"c"}`,
	`{"objectType":"VEN_RESOURCE_REQUEST","resourceName":"r","venID":"v"}`,
	`{"clientName":"c","programID":"p","objectOperations":[{"objects":["EVENT"],"operations":["POST"],"callbackUrl":"https://x","bearerToken":"t","mechanism":"MQTT"}]}`,
	`{"objectType":"EVENT","operation":"POST","object":{"programID":"p","intervals":[]},"targets":["a"]}`,
	`{"objectType":"RESOURCE_GROUP","operation":"DELETE","object":{"resourceGroupName":"g","children":[{"objectType":"RESOURCE","objectID":"r"}]}}`,
	`{"WEBHOOK":true,"MQTT":{"URIS":["mqtt://b:1883"],"serialization":"JSON","authentication":{"method":"OAUTH2_BEARER_TOKEN"}}}`,
	`{"topics":{"CREATE":"a","UPDATE":"b","DELETE":"c","ALL":"d"}}`,
	`{"type":"about:blank","title":"t","status":400,"detail":"d"}`,
	`{"tokenURL":"https://idp/token"}`,
	`[{"programID":"p","intervals":[]}]`,
	`{"intervals":[{"id":2147483648,"payloads":[]}]}`,
	`{"values":[1e309]}`,
	`null`, ``, `{`, `[]`,
}

// FuzzCodec feeds untrusted bytes to every decoder of the 3.1.0 codec; kind
// selects which.
func FuzzCodec(f *testing.F) {
	for _, s := range fuzzSeeds {
		for kind := range uint8(13) {
			f.Add(kind, []byte(s))
		}
	}
	c := Codec()
	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		switch kind % 13 {
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
			fuzzObject(t, c.ResourceGroups, data, openadr.ResourceGroup.Validate)
		case 7:
			fuzzOther(t, data, c.UnmarshalNotification, c.MarshalNotification)
		case 8:
			fuzzOther(t, data, c.UnmarshalNotifiers, c.MarshalNotifiers)
		case 9:
			fuzzOther(t, data, c.UnmarshalTopics, c.MarshalTopics)
		case 10:
			fuzzOther(t, data, c.UnmarshalProblem, c.MarshalProblem)
		case 11:
			fuzzOther(t, data, c.UnmarshalAuthServer, c.MarshalAuthServer)
		case 12:
			// A list fails as a whole or decodes every item.
			if vs, err := c.Events.UnmarshalList(data); err == nil {
				if _, err := c.Events.MarshalList(vs); err != nil {
					var ve *openadr.ValidationError
					if !errors.As(err, &ve) {
						t.Fatalf("MarshalList of decoded events: %v", err)
					}
				}
			}
		}
	})
}
