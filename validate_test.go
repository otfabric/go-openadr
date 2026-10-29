// SPDX-License-Identifier: MIT

package openadr

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"object-999": true, "a": true, "A_b-9": true, strings.Repeat("x", 128): true,
		"": false, strings.Repeat("x", 129): false, "a b": false, "a/b": false, "a.b": false, "é": false, "../x": false,
	} {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v", id, got)
		}
	}
}

func TestValidate(t *testing.T) {
	long := strings.Repeat("x", 129)
	descriptor := NewReportDescriptor("USAGE")
	hook := []ObjectOperation{{Objects: []ObjectType{ObjectEvent}, Operations: []Operation{OperationCreate}, CallbackURL: "https://example.com/h"}}
	valid := []interface{ Validate() error }{
		Program{Name: "p"},
		Program{Name: "p", Descriptions: []ProgramDescription{{URL: "https://example.com"}}, Targets: []string{"a"},
			PayloadDescriptors: []PayloadDescriptor{EventPayloadDescriptor{PayloadType: "PRICE"}, ReportPayloadDescriptor{PayloadType: "USAGE", Confidence: new(int)}},
			Attributes:         []ValuesMap{{Type: "X", Values: []Value{Number(1), String(""), Bool(false), PointValue(0, 0)}}}},
		Event{ProgramID: "p1"},
		Event{ProgramID: "p1", Priority: new(int), ReportDescriptors: []ReportDescriptor{descriptor}, Intervals: []Interval{{ID: -1}}},
		Report{EventID: "e1", ClientName: "c"},
		VEN{Name: "v"},
		Resource{Name: "r", VENID: "v1"},
		Subscription{ClientName: "c"},
		Subscription{ClientName: "c", ProgramID: "p1", ObjectOperations: hook, Targets: []string{"a"}},
	}
	for _, v := range valid {
		if err := v.Validate(); err != nil {
			t.Errorf("%+v: %v", v, err)
		}
	}

	negative, hundredOne, nan := -1, 101, math.NaN()
	bad := descriptor
	bad.ReportIntervals = ""
	for field, v := range map[string]interface{ Validate() error }{
		"programName":                          Program{},
		"programName ":                         Program{Name: long},
		"programDescriptions[0].URL":           Program{Name: "p", Descriptions: []ProgramDescription{{URL: "x"}}},
		"payloadDescriptors[0]":                Program{Name: "p", PayloadDescriptors: []PayloadDescriptor{nil}},
		"payloadDescriptors[0].payloadType":    Program{Name: "p", PayloadDescriptors: []PayloadDescriptor{EventPayloadDescriptor{}}},
		"payloadDescriptors[0].confidence":     Program{Name: "p", PayloadDescriptors: []PayloadDescriptor{ReportPayloadDescriptor{PayloadType: "U", Confidence: &hundredOne}}},
		"payloadDescriptors[0].accuracy":       Program{Name: "p", PayloadDescriptors: []PayloadDescriptor{ReportPayloadDescriptor{PayloadType: "U", Accuracy: &nan}}},
		"attributes[0].type":                   Program{Name: "p", Attributes: []ValuesMap{{}}},
		"attributes[0].values[0]":              Program{Name: "p", Attributes: []ValuesMap{{Type: "X", Values: []Value{{}}}}},
		"attributes[0].values[1]":              Program{Name: "p", Attributes: []ValuesMap{{Type: "X", Values: []Value{Number(1), Number(math.Inf(1))}}}},
		"targets[1]":                           Program{Name: "p", Targets: []string{"a", ""}},
		"programID":                            Event{},
		"programID ":                           Event{ProgramID: "a/b"},
		"priority":                             Event{ProgramID: "p", Priority: &negative},
		"reportDescriptors[0].payloadType":     Event{ProgramID: "p", ReportDescriptors: []ReportDescriptor{{}}},
		"reportDescriptors[0].reportIntervals": Event{ProgramID: "p", ReportDescriptors: []ReportDescriptor{bad}},
		"intervals[0].payloads[0].type":        Event{ProgramID: "p", Intervals: []Interval{{Payloads: []ValuesMap{{}}}}},
		"eventID":                              Report{ClientName: "c"},
		"clientName":                           Report{EventID: "e"},
		"resources[0].resourceName":            Report{EventID: "e", ClientName: "c", Resources: []ReportResource{{}}},
		"resources[0].intervals[0].payloads[0].values[0]": Report{EventID: "e", ClientName: "c", Resources: []ReportResource{{ResourceName: "r",
			Intervals: []Interval{{Payloads: []ValuesMap{{Type: "X", Values: []Value{PointValue(nan, 0)}}}}}}}},
		"venName":                           VEN{},
		"clientID":                          VEN{Name: "v", ClientID: long},
		"resourceName":                      Resource{VENID: "v"},
		"venID":                             Resource{Name: "r"},
		"clientName ":                       Subscription{},
		"programID  ":                       Subscription{ClientName: "c", ProgramID: "a b"},
		"objectOperations[0].callbackUrl":   Subscription{ClientName: "c", ObjectOperations: []ObjectOperation{{}}},
		"objectOperations[0].objects[0]":    Subscription{ClientName: "c", ObjectOperations: []ObjectOperation{{Objects: []ObjectType{"TARIFF"}, CallbackURL: "https://x"}}},
		"objectOperations[0].operations[0]": Subscription{ClientName: "c", ObjectOperations: []ObjectOperation{{Operations: []Operation{"PATCH"}, CallbackURL: "https://x"}}},
		"targets[0]":                        Subscription{ClientName: "c", Targets: []string{long}},
	} {
		var invalid *ValidationError
		if err := v.Validate(); !errors.As(err, &invalid) || invalid.Field != strings.TrimSpace(field) {
			t.Errorf("%+v: %v, want a ValidationError for %s", v, err, strings.TrimSpace(field))
		}
	}
}

func TestErrorAndValue(t *testing.T) {
	err := &Error{Method: "GET", Path: "/programs/x", StatusCode: 404, Problem: Problem{Title: "Not Found", Detail: "gone"}}
	if err.Error() != "openadr: GET /programs/x: 404 Not Found: gone" || !errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		t.Errorf("Error = %q", err)
	}
	if bare := (&Error{Method: "PUT", Path: "/x", StatusCode: 502}); bare.Error() != "openadr: PUT /x: 502 Bad Gateway" || errors.Is(bare, ErrBadRequest) {
		t.Errorf("Error without a problem = %q", bare)
	}
	for status, sentinel := range map[int]error{400: ErrBadRequest, 401: ErrUnauthorized, 403: ErrForbidden, 404: ErrNotFound, 409: ErrConflict} {
		if !errors.Is(&Error{StatusCode: status}, sentinel) {
			t.Errorf("status %d does not match %v", status, sentinel)
		}
	}
	for v, want := range map[Value]string{Number(0.5): "0.5", Number(3): "3", String("a"): `"a"`, Bool(true): "true", PointValue(1, 2.5): "(1,2.5)", {}: "<invalid>"} {
		if v.String() != want {
			t.Errorf("%#v.String() = %q, want %q", v, v.String(), want)
		}
	}
	if n, ok := String("x").Number(); ok || n != 0 {
		t.Error("a string Value answered as a number")
	}
	if p, ok := PointValue(1, 2).Point(); !ok || p != (Point{1, 2}) || PointValue(1, 2).Kind().String() != "point" || (Value{}).Kind().String() != "invalid" {
		t.Error("Value accessors")
	}
	if s, ok := String("x").Text(); !ok || s != "x" {
		t.Error("Text")
	}
	if b, ok := Bool(true).Bool(); !ok || !b {
		t.Error("Bool")
	}
}

func TestValidateExtensions(t *testing.T) {
	ok := []interface{ Validate() error }{
		ResourceGroup{Name: "g"},
		ResourceGroup{Name: "g", Targets: []string{"t"}, Attributes: []ValuesMap{{Type: "A", Values: []Value{Number(1)}}},
			Children: []ResourceGroupChild{{Type: ChildResource, ID: "r1"}, {Type: ChildResourceGroup, ID: "g1"}}},
		Subscription{ClientName: "c", ObjectOperations: []ObjectOperation{{Mechanism: MechanismWebSocket}}},
		Subscription{ClientName: "c", ObjectOperations: []ObjectOperation{{Mechanism: MechanismWebhook, CallbackURL: "https://example.com/"}}},
	}
	for i, v := range ok {
		if err := v.Validate(); err != nil {
			t.Errorf("valid object %d: %v", i, err)
		}
	}
	bad := map[string]interface{ Validate() error }{
		"resourceGroupName":               ResourceGroup{},
		"children[0].type":                ResourceGroup{Name: "g", Children: []ResourceGroupChild{{Type: "device", ID: "x"}}},
		"children[1].id":                  ResourceGroup{Name: "g", Children: []ResourceGroupChild{{Type: ChildResource, ID: "r"}, {Type: ChildResource, ID: "not an id"}}},
		"targets[0]":                      ResourceGroup{Name: "g", Targets: []string{""}},
		"attributes[0].type":              ResourceGroup{Name: "g", Attributes: []ValuesMap{{}}},
		"objectOperations[0].mechanism":   Subscription{ClientName: "c", ObjectOperations: []ObjectOperation{{Mechanism: "PIGEON", CallbackURL: "https://example.com/"}}},
		"objectOperations[0].callbackUrl": Subscription{ClientName: "c", ObjectOperations: []ObjectOperation{{Mechanism: MechanismWebSocket, CallbackURL: "https://example.com/"}}},
		"objectOperations[1].callbackUrl": Subscription{ClientName: "c", ObjectOperations: []ObjectOperation{{Mechanism: MechanismWebSocket}, {Mechanism: MechanismWebhook}}},
	}
	for field, v := range bad {
		var invalid *ValidationError
		if err := v.Validate(); !errors.As(err, &invalid) || invalid.Field != field {
			t.Errorf("got %v, want a fault in %s", err, field)
		}
	}
}

// Every failure a caller can meet matches a sentinel, and only its own.
func TestSentinels(t *testing.T) {
	all := []error{ErrBadRequest, ErrUnauthorized, ErrForbidden, ErrNotFound, ErrConflict, ErrTooManyRequests,
		ErrServer, ErrNotImplemented, ErrUnavailable, ErrInvalid, ErrConfiguration, ErrTimeout, ErrProtocol, ErrClosed}
	for status, want := range map[int][]error{
		400: {ErrBadRequest}, 401: {ErrUnauthorized}, 403: {ErrForbidden}, 404: {ErrNotFound}, 409: {ErrConflict},
		429: {ErrTooManyRequests}, 500: {ErrServer}, 501: {ErrNotImplemented, ErrServer},
		502: {ErrUnavailable, ErrServer}, 503: {ErrUnavailable, ErrServer}, 504: {ErrUnavailable, ErrServer},
		507: {ErrServer}, 405: nil, 413: nil, 426: nil,
	} {
		err := error(&Error{Method: "GET", Path: "/x", StatusCode: status})
		for _, sentinel := range all {
			wanted := false
			for _, w := range want {
				wanted = wanted || w == sentinel
			}
			if errors.Is(err, sentinel) != wanted {
				t.Errorf("a %d matches %v: %v, want %v", status, sentinel, !wanted, wanted)
			}
		}
	}
	invalid := error(&ValidationError{Field: "f", Reason: "r"})
	if !errors.Is(invalid, ErrInvalid) || errors.Is(invalid, ErrBadRequest) {
		t.Error("a ValidationError matches ErrInvalid and nothing else")
	}
	if err := (Program{}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate: %v", err)
	}
	// Each has a text of its own, which begins as every error here does.
	seen := map[string]bool{}
	for _, sentinel := range all {
		if !strings.HasPrefix(sentinel.Error(), "openadr: ") || seen[sentinel.Error()] {
			t.Errorf("sentinel %q", sentinel)
		}
		seen[sentinel.Error()] = true
	}
}

// TestValidate301 checks the properties only OpenADR 3.0.1 has: typed
// targets are values maps, and the program of a report an object ID.
func TestValidate301(t *testing.T) {
	typed := []ValuesMap{{Type: "GROUP", Values: []Value{String("g")}}}
	untyped := []ValuesMap{{Values: []Value{String("g")}}}
	descriptor := NewReportDescriptor("USAGE")
	descriptor.TypedTargets = untyped
	ok := []interface{ Validate() error }{
		Program{Name: "p", TypedTargets: typed},
		Event{ProgramID: "p", TypedTargets: typed},
		VEN{Name: "v", TypedTargets: typed},
		Resource{Name: "r", VENID: "v", TypedTargets: typed},
		Subscription{ClientName: "c", TypedTargets: typed},
		Report{EventID: "e", ClientName: "c", ProgramID: "p"},
	}
	for i, v := range ok {
		if err := v.Validate(); err != nil {
			t.Errorf("valid object %d: %v", i, err)
		}
	}
	bad := map[string]interface{ Validate() error }{
		"program":    Program{Name: "p", TypedTargets: untyped},
		"event":      Event{ProgramID: "p", TypedTargets: untyped},
		"descriptor": Event{ProgramID: "p", ReportDescriptors: []ReportDescriptor{descriptor}},
		"VEN":        VEN{Name: "v", TypedTargets: untyped},
		"resource":   Resource{Name: "r", VENID: "v", TypedTargets: untyped},
		"sub":        Subscription{ClientName: "c", TypedTargets: untyped},
		"report":     Report{EventID: "e", ClientName: "c", ProgramID: "not an id"},
	}
	for name, v := range bad {
		if err := v.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}
