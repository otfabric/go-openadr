// SPDX-License-Identifier: MIT

package objectjson

import (
	"errors"
	"strings"
	"testing"

	openadr "github.com/otfabric/go-openadr"
)

func TestRevisions(t *testing.T) {
	typed := openadr.Program{Name: "p", TypedTargets: []openadr.ValuesMap{{Type: "GROUP", Values: []openadr.Value{openadr.String("g1")}}},
		Attributes: []openadr.ValuesMap{{Type: "COUNTRY", Values: []openadr.Value{openadr.String("NL")}}}}
	data, err := Marshal(openadr.Version301, typed)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"targets":[{"type":"GROUP","values":["g1"]}]`, `"country":"NL"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("3.0.1 JSON %s has no %s", data, want)
		}
	}
	back, err := Unmarshal[openadr.Program](openadr.Version301, data)
	if err != nil || back.TypedTargets[0].Values[0] != openadr.String("g1") || back.Attributes[0].Type != "COUNTRY" {
		t.Errorf("Unmarshal = %+v, %v", back, err)
	}
	if _, err := Marshal("", typed); !errors.Is(err, openadr.ErrInvalid) {
		t.Errorf("typed targets in 3.1.0: %v, want ErrInvalid", err)
	}

	plain := openadr.Program{Name: "p", Targets: []string{"g1"}}
	data, err = Marshal(openadr.Version310, plain)
	if err != nil || !strings.Contains(string(data), `"targets":["g1"]`) {
		t.Errorf("3.1.0 JSON = %s, %v", data, err)
	}
	if _, err := Marshal(openadr.Version301, plain); !errors.Is(err, openadr.ErrInvalid) {
		t.Errorf("string targets in 3.0.1: %v, want ErrInvalid", err)
	}
	if _, err := Marshal(openadr.Version301, openadr.ResourceGroup{Name: "g"}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("a resource group in 3.0.1: %v, want ErrUnsupported", err)
	}
	if _, err := Marshal("3.0.2", plain); !errors.Is(err, openadr.ErrConfiguration) {
		t.Errorf("3.0.2: %v, want ErrConfiguration", err)
	}
	if _, err := Unmarshal[openadr.Event]("3.0.2", nil); !errors.Is(err, openadr.ErrConfiguration) {
		t.Errorf("3.0.2: %v, want ErrConfiguration", err)
	}
}

func TestEveryKind(t *testing.T) {
	sub := openadr.Subscription{ClientName: "c", ProgramID: "p1",
		ObjectOperations: []openadr.ObjectOperation{{Objects: []openadr.ObjectType{openadr.ObjectEvent},
			Operations: []openadr.Operation{openadr.OperationCreate}, CallbackURL: "https://ven.example/cb"}}}
	for _, v := range []openadr.Version{openadr.Version310, openadr.Version301} {
		roundTrip(t, v, openadr.Event{ProgramID: "p1", Intervals: []openadr.Interval{}})
		roundTrip(t, v, openadr.Report{ProgramID: map[openadr.Version]string{openadr.Version301: "p1"}[v], EventID: "e1", ClientName: "c", Resources: []openadr.ReportResource{}})
		roundTrip(t, v, openadr.VEN{Name: "v"})
		roundTrip(t, v, openadr.Resource{Name: "r", VENID: "v1"})
		roundTrip(t, v, sub)
		roundTrip(t, v, openadr.Notification{ObjectType: openadr.ObjectVEN, Operation: openadr.OperationDelete, Object: openadr.VEN{Name: "v"}})
	}
	roundTrip(t, openadr.Version310, openadr.ResourceGroup{Name: "g"})
}

func roundTrip[T Object](t *testing.T, v openadr.Version, obj T) {
	t.Helper()
	data, err := Marshal(v, obj)
	if err != nil {
		t.Errorf("%s %T: %v", v, obj, err)
		return
	}
	if _, err := Unmarshal[T](v, data); err != nil {
		t.Errorf("%s %T: reading %s: %v", v, obj, data, err)
	}
}
