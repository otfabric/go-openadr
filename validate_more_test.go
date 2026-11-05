// SPDX-License-Identifier: MIT

package openadr

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// TestValidateNested reaches the faults of nested properties that
// TestValidate does not: each must name the property it is about.
func TestValidateNested(t *testing.T) {
	long := strings.Repeat("é", 129) // 129 characters, 258 bytes
	fits := strings.Repeat("é", 128)
	nan := math.NaN()
	descriptor := NewReportDescriptor("USAGE")
	with := func(change func(*ReportDescriptor)) []ReportDescriptor {
		d := descriptor
		change(&d)
		return []ReportDescriptor{d}
	}
	typed := []ValuesMap{{Type: "GROUP", Values: []Value{{}}}}
	for field, v := range map[string]interface{ Validate() error }{
		"payloadDescriptors[0].readingType":    Program{Name: "p", PayloadDescriptors: []PayloadDescriptor{ReportPayloadDescriptor{PayloadType: "U", ReadingType: long}}},
		"payloadDescriptors[0].units":          Program{Name: "p", PayloadDescriptors: []PayloadDescriptor{ReportPayloadDescriptor{PayloadType: "U", Units: long}}},
		"payloadDescriptors[0].units ":         Program{Name: "p", PayloadDescriptors: []PayloadDescriptor{EventPayloadDescriptor{PayloadType: "U", Units: long}}},
		"targets[0].values[0]":                 Program{Name: "p", TypedTargets: typed},
		"programDescriptions[0].URL":           Program{Name: "p", Descriptions: []ProgramDescription{{URL: strings.Repeat("u", 8001)}}},
		"targets[0]":                           Event{ProgramID: "p", Targets: []string{""}},
		"targets[0].values[0] ":                Event{ProgramID: "p", TypedTargets: typed},
		"reportDescriptors[0].readingType":     Event{ProgramID: "p", ReportDescriptors: with(func(d *ReportDescriptor) { d.ReadingType = long })},
		"reportDescriptors[0].units":           Event{ProgramID: "p", ReportDescriptors: with(func(d *ReportDescriptor) { d.Units = long })},
		"reportDescriptors[0].targets[0]":      Event{ProgramID: "p", ReportDescriptors: with(func(d *ReportDescriptor) { d.Targets = []string{""} })},
		"reportDescriptors[0].targets[0].type": Event{ProgramID: "p", ReportDescriptors: with(func(d *ReportDescriptor) { d.TypedTargets = []ValuesMap{{}} })},
		"reportDescriptors[0].reportIntervals": Event{ProgramID: "p", ReportDescriptors: with(func(d *ReportDescriptor) { d.ReportIntervals = "SOMETIMES" })},
		"payloadDescriptors[0].payloadType":    Event{ProgramID: "p", PayloadDescriptors: []EventPayloadDescriptor{{}}},
		"programID":                            Report{EventID: "e", ClientName: "c", ProgramID: "a b"},
		"payloadDescriptors[0].accuracy":       Report{EventID: "e", ClientName: "c", PayloadDescriptors: []ReportPayloadDescriptor{{PayloadType: "U", Accuracy: &nan}}},
		"clientName":                           Report{EventID: "e", ClientName: long},
		"attributes[0].type":                   VEN{Name: "v", Attributes: []ValuesMap{{Type: long}}},
		"targets[0] ":                          VEN{Name: "v", Targets: []string{long}},
		"targets[0].type":                      VEN{Name: "v", TypedTargets: []ValuesMap{{}}},
		"clientID":                             Resource{Name: "r", VENID: "v", ClientID: long},
		"attributes[0].values[0]":              Resource{Name: "r", VENID: "v", Attributes: []ValuesMap{{Type: "X", Values: []Value{Number(math.Inf(-1))}}}},
		"targets[0]  ":                         Resource{Name: "r", VENID: "v", Targets: []string{""}},
		"targets[0].type ":                     Resource{Name: "r", VENID: "v", TypedTargets: []ValuesMap{{}}},
		"resourceGroupName":                    ResourceGroup{},
		"attributes[0].type ":                  ResourceGroup{Name: "g", Attributes: []ValuesMap{{}}},
		"children[0].type":                     ResourceGroup{Name: "g", Children: []ResourceGroupChild{{Type: "VEN", ID: "v"}}},
		"children[0].id":                       ResourceGroup{Name: "g", Children: []ResourceGroupChild{{Type: ChildResource, ID: "a b"}}},
		"targets[0]   ":                        ResourceGroup{Name: "g", Targets: []string{""}},
	} {
		var invalid *ValidationError
		if err := v.Validate(); !errors.As(err, &invalid) || invalid.Field != strings.TrimSpace(field) {
			t.Errorf("%+v: %v, want a ValidationError for %s", v, err, strings.TrimSpace(field))
		}
	}
	// Limits are in characters, not bytes.
	for _, v := range []interface{ Validate() error }{
		Program{Name: fits}, VEN{Name: fits, ClientID: fits}, ResourceGroup{Name: fits, Children: []ResourceGroupChild{{Type: ChildResourceGroup, ID: "g"}}},
	} {
		if err := v.Validate(); err != nil {
			t.Errorf("%+v: %v", v, err)
		}
	}
}
