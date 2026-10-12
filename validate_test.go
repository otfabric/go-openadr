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
