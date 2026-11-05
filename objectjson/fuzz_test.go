// SPDX-License-Identifier: MIT

package objectjson_test

import (
	"bytes"
	"errors"
	"testing"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/objectjson"
)

// roundTrip decodes untrusted data as a T of revision v. What decodes and
// validates encodes, unless the revision cannot carry it, and what that
// encodes decodes and encodes to the same bytes.
func roundTrip[T objectjson.Object](t *testing.T, v openadr.Version, data []byte, validate func(T) error) {
	t.Helper()
	obj, err := objectjson.Unmarshal[T](v, data)
	if err != nil {
		var ve *openadr.ValidationError
		if !errors.As(err, &ve) && !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("Unmarshal[%T](%s, %q): %v", obj, v, data, err)
		}
		return
	}
	if validate != nil && validate(obj) != nil {
		return
	}
	out, err := objectjson.Marshal(v, obj)
	if err != nil {
		var ve *openadr.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("Marshal(%s, %+v): %v", v, obj, err)
		}
		return
	}
	again, err := objectjson.Unmarshal[T](v, out)
	if err != nil {
		t.Fatalf("re-decoding %s: %v", out, err)
	}
	if out2, err := objectjson.Marshal(v, again); err != nil || !bytes.Equal(out, out2) {
		t.Fatalf("encoding is not stable (%v):\n%s\n%s", err, out, out2)
	}
}

func FuzzUnmarshal(f *testing.F) {
	for _, s := range []string{
		`{"programName":"p","targets":["a"]}`,
		`{"programName":"p","targets":[{"type":"GROUP","values":["g"]}],"timeZoneOffset":"PT1H"}`,
		`{"programID":"p","intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[1,"a",true,{"x":1,"y":2}]}]}]}`,
		`{"programID":"p","eventID":"e","clientName":"c","resources":[]}`,
		`{"objectType":"BL_VEN_REQUEST","venName":"v"}`,
		`{"resourceName":"r","venID":"v"}`,
		`{"programID":"p","clientName":"c","objectOperations":[{"objects":["EVENT"],"operations":["GET"],"callbackUrl":"https://x"}]}`,
		`{"resourceGroupName":"g"}`,
		`{"objectType":"VEN","operation":"PUT","object":{"venName":"v"}}`,
	} {
		for kind := range uint8(16) {
			f.Add(kind, []byte(s))
		}
	}
	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		v := openadr.Version310
		if kind&8 != 0 {
			v = openadr.Version301
		}
		switch kind % 8 {
		case 0:
			roundTrip(t, v, data, openadr.Program.Validate)
		case 1:
			roundTrip(t, v, data, openadr.Event.Validate)
		case 2:
			roundTrip(t, v, data, openadr.Report.Validate)
		case 3:
			roundTrip(t, v, data, openadr.VEN.Validate)
		case 4:
			roundTrip(t, v, data, openadr.Resource.Validate)
		case 5:
			roundTrip(t, v, data, openadr.Subscription.Validate)
		case 6:
			roundTrip(t, v, data, openadr.ResourceGroup.Validate)
		case 7:
			roundTrip[openadr.Notification](t, v, data, nil)
		}
	})
}

// TestRefusesInvalidUTF8 checks that every object of both revisions is
// refused when its body is not UTF-8, rather than read with U+FFFD.
func TestRefusesInvalidUTF8(t *testing.T) {
	for _, v := range []openadr.Version{openadr.Version310, openadr.Version301} {
		for name, err := range map[string]error{
			"program": func() error {
				_, err := objectjson.Unmarshal[openadr.Program](v, []byte("{\"programName\":\"\xff\xfe\"}"))
				return err
			}(),
			"event": func() error {
				_, err := objectjson.Unmarshal[openadr.Event](v, []byte("{\"programID\":\"p\",\"eventName\":\"\xff\",\"intervals\":[]}"))
				return err
			}(),
			"ven": func() error {
				_, err := objectjson.Unmarshal[openadr.VEN](v, []byte("{\"venName\":\"\xc3\"}"))
				return err
			}(),
			"notification": func() error {
				_, err := objectjson.Unmarshal[openadr.Notification](v, []byte("{\"objectType\":\"VEN\",\"operation\":\"POST\",\"object\":{\"venName\":\"\xff\"}}"))
				return err
			}(),
		} {
			var ve *openadr.ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("%s %s: %v, want a ValidationError", v, name, err)
			}
		}
	}
}
