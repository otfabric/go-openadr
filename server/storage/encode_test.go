// SPDX-License-Identifier: MIT

package storage

import (
	"reflect"
	"strings"
	"testing"

	openadr "github.com/otfabric/go-openadr"
)

func TestEncodeDecode(t *testing.T) {
	program := openadr.Program{Metadata: meta("p1"), Name: "tariff", Targets: []string{"a"},
		PayloadDescriptors: []openadr.PayloadDescriptor{openadr.EventPayloadDescriptor{PayloadType: "PRICE"}}}
	b, err := Encode(program)
	if err != nil {
		t.Fatal(err)
	}
	// The form is the object of the API, which is what makes it stable.
	if !strings.Contains(string(b), `"programName":"tariff"`) || !strings.Contains(string(b), `"objectType":"PROGRAM"`) {
		t.Errorf("encoded as %s", b)
	}
	if got, err := Decode[openadr.Program](b); err != nil || !reflect.DeepEqual(got, program) {
		t.Errorf("decoded %+v, %v", got, err)
	}

	rec := SubscriptionRecord{ReadsAll: true, Subscription: openadr.Subscription{Metadata: meta("s1"), ClientID: "bl", ClientName: "n",
		ObjectOperations: []openadr.ObjectOperation{{Objects: []openadr.ObjectType{openadr.ObjectEvent}, Operations: []openadr.Operation{openadr.OperationCreate}, Mechanism: openadr.MechanismWebSocket}}}}
	b, err = Encode(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Decode[SubscriptionRecord](b); err != nil || !reflect.DeepEqual(got, rec) {
		t.Errorf("decoded %+v, %v from %s", got, err, b)
	}

	// One of every kind goes through.
	for _, roundTrip := range []func() error{
		func() error {
			b, _ := Encode(openadr.Event{Metadata: meta("e"), ProgramID: "p"})
			_, err := Decode[openadr.Event](b)
			return err
		},
		func() error {
			b, _ := Encode(openadr.Report{Metadata: meta("r"), EventID: "e"})
			_, err := Decode[openadr.Report](b)
			return err
		},
		func() error {
			b, _ := Encode(openadr.VEN{Metadata: meta("v"), Name: "n"})
			_, err := Decode[openadr.VEN](b)
			return err
		},
		func() error {
			b, _ := Encode(openadr.Resource{Metadata: meta("x"), Name: "n", VENID: "v"})
			_, err := Decode[openadr.Resource](b)
			return err
		},
		func() error {
			b, _ := Encode(openadr.ResourceGroup{Metadata: meta("g"), Name: "n"})
			_, err := Decode[openadr.ResourceGroup](b)
			return err
		},
	} {
		if err := roundTrip(); err != nil {
			t.Error(err)
		}
	}

	// What is not an object is an error that says of what.
	for _, bad := range []string{``, `nonsense`, `[]`, `{"programName":7}`} {
		if _, err := Decode[openadr.Program]([]byte(bad)); err == nil || !strings.Contains(err.Error(), "openadr.Program") {
			t.Errorf("decoding %q: %v", bad, err)
		}
	}
	if _, err := Decode[SubscriptionRecord]([]byte(`{"subscription":"x"}`)); err == nil {
		t.Error("a subscription record without a subscription was decoded")
	}
	if _, err := Encode(openadr.Program{Attributes: []openadr.ValuesMap{{Type: "A", Values: []openadr.Value{{}}}}}); err == nil {
		t.Error("a value that is not set was encoded")
	}
}
