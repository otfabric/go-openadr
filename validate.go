// SPDX-License-Identifier: MIT

package openadr

import (
	"fmt"
	"math"
	"unicode/utf8"
)

// Limits of the OpenADR 3.1.0 schema.
const (
	maxNameLen = 128
	minURLLen  = 2
	maxURLLen  = 8000
)

// ValidID reports whether id is a well-formed object ID: 1 to 128 characters
// of letters, digits, '_' and '-'.
func ValidID(id string) bool {
	if len(id) == 0 || len(id) > maxNameLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// name checks a string limited to 1..128 characters.
func name(field, s string, required bool) error {
	if s == "" {
		if required {
			return invalid(field, "is required")
		}
		return nil
	}
	if utf8.RuneCountInString(s) > maxNameLen {
		return invalid(field, "is longer than %d characters", maxNameLen)
	}
	return nil
}

func id(field, s string) error {
	if s == "" {
		return invalid(field, "is required")
	}
	if !ValidID(s) {
		return invalid(field, "is not an object ID (1 to %d characters of [a-zA-Z0-9_-])", maxNameLen)
	}
	return nil
}

func targets(field string, ts []string) error {
	for i, t := range ts {
		if t == "" {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "is empty")
		}
		if err := name(fmt.Sprintf("%s[%d]", field, i), t, true); err != nil {
			return err
		}
	}
	return nil
}

func valuesMaps(field string, vms []ValuesMap) error {
	for i, vm := range vms {
		f := fmt.Sprintf("%s[%d]", field, i)
		if err := name(f+".type", vm.Type, true); err != nil {
			return err
		}
		for j, v := range vm.Values {
			vf := fmt.Sprintf("%s.values[%d]", f, j)
			switch v.kind {
			case KindNumber:
				if math.IsNaN(v.num) || math.IsInf(v.num, 0) {
					return invalid(vf, "is not a finite number")
				}
			case KindPoint:
				if math.IsNaN(v.pt.X) || math.IsInf(v.pt.X, 0) || math.IsNaN(v.pt.Y) || math.IsInf(v.pt.Y, 0) {
					return invalid(vf, "is not a finite point")
				}
			case KindString, KindBool:
			default:
				return invalid(vf, "is the zero Value")
			}
		}
	}
	return nil
}

func intervals(field string, ivs []Interval) error {
	for i, iv := range ivs {
		if err := valuesMaps(fmt.Sprintf("%s[%d].payloads", field, i), iv.Payloads); err != nil {
			return err
		}
	}
	return nil
}

func (d EventPayloadDescriptor) validate(field string) error {
	if err := name(field+".payloadType", d.PayloadType, true); err != nil {
		return err
	}
	return name(field+".units", d.Units, false)
}

func (d ReportPayloadDescriptor) validate(field string) error {
	if err := name(field+".payloadType", d.PayloadType, true); err != nil {
		return err
	}
	if err := name(field+".readingType", d.ReadingType, false); err != nil {
		return err
	}
	if err := name(field+".units", d.Units, false); err != nil {
		return err
	}
	if d.Confidence != nil && (*d.Confidence < 0 || *d.Confidence > 100) {
		return invalid(field+".confidence", "is outside 0..100")
	}
	if d.Accuracy != nil && (math.IsNaN(*d.Accuracy) || math.IsInf(*d.Accuracy, 0)) {
		return invalid(field+".accuracy", "is not a finite number")
	}
	return nil
}

// Validate checks the client-provided properties against the schema. It does
// not look at Metadata.
func (p Program) Validate() error {
	if err := name("programName", p.Name, true); err != nil {
		return err
	}
	for i, d := range p.Descriptions {
		if n := len(d.URL); n < minURLLen || n > maxURLLen {
			return invalid(fmt.Sprintf("programDescriptions[%d].URL", i), "must be %d to %d characters", minURLLen, maxURLLen)
		}
	}
	for i, d := range p.PayloadDescriptors {
		f := fmt.Sprintf("payloadDescriptors[%d]", i)
		var err error
		switch d := d.(type) {
		case EventPayloadDescriptor:
			err = d.validate(f)
		case ReportPayloadDescriptor:
			err = d.validate(f)
		default:
			err = invalid(f, "is nil")
		}
		if err != nil {
			return err
		}
	}
	if err := valuesMaps("attributes", p.Attributes); err != nil {
		return err
	}
	if err := targets("targets", p.Targets); err != nil {
		return err
	}
	return valuesMaps("targets", p.TypedTargets)
}

// Validate checks the client-provided properties against the schema. It does
// not look at Metadata.
func (e Event) Validate() error {
	if err := id("programID", e.ProgramID); err != nil {
		return err
	}
	if e.Priority != nil && *e.Priority < 0 {
		return invalid("priority", "is negative")
	}
	if err := targets("targets", e.Targets); err != nil {
		return err
	}
	if err := valuesMaps("targets", e.TypedTargets); err != nil {
		return err
	}
	for i, d := range e.ReportDescriptors {
		f := fmt.Sprintf("reportDescriptors[%d]", i)
		if err := name(f+".payloadType", d.PayloadType, true); err != nil {
			return err
		}
		if err := name(f+".readingType", d.ReadingType, false); err != nil {
			return err
		}
		if err := name(f+".units", d.Units, false); err != nil {
			return err
		}
		if err := targets(f+".targets", d.Targets); err != nil {
			return err
		}
		if err := valuesMaps(f+".targets", d.TypedTargets); err != nil {
			return err
		}
		switch d.ReportIntervals {
		case ReportIntervalsIntervals, ReportIntervalsSubIntervals, ReportIntervalsOpen:
		default:
			return invalid(f+".reportIntervals", "is %q; build descriptors with NewReportDescriptor", d.ReportIntervals)
		}
	}
	for i, d := range e.PayloadDescriptors {
		if err := d.validate(fmt.Sprintf("payloadDescriptors[%d]", i)); err != nil {
			return err
		}
	}
	return intervals("intervals", e.Intervals)
}

// Validate checks the client-provided properties against the schema. It does
// not look at Metadata or ClientID, which the VTN writes.
func (r Report) Validate() error {
	if err := id("eventID", r.EventID); err != nil {
		return err
	}
	if err := name("clientName", r.ClientName, true); err != nil {
		return err
	}
	if r.ProgramID != "" && !ValidID(r.ProgramID) {
		return invalid("programID", "is not an object ID")
	}
	for i, d := range r.PayloadDescriptors {
		if err := d.validate(fmt.Sprintf("payloadDescriptors[%d]", i)); err != nil {
			return err
		}
	}
	for i, res := range r.Resources {
		f := fmt.Sprintf("resources[%d]", i)
		if err := name(f+".resourceName", res.ResourceName, true); err != nil {
			return err
		}
		if err := intervals(f+".intervals", res.Intervals); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks the client-provided properties against the schema. ClientID
// is not required here: whether it is depends on who sends the object.
func (v VEN) Validate() error {
	if err := name("venName", v.Name, true); err != nil {
		return err
	}
	if err := name("clientID", v.ClientID, false); err != nil {
		return err
	}
	if err := valuesMaps("attributes", v.Attributes); err != nil {
		return err
	}
	if err := targets("targets", v.Targets); err != nil {
		return err
	}
	return valuesMaps("targets", v.TypedTargets)
}

// Validate checks the client-provided properties against the schema. ClientID
// is not required here: whether it is depends on who sends the object.
func (r Resource) Validate() error {
	if err := name("resourceName", r.Name, true); err != nil {
		return err
	}
	if err := id("venID", r.VENID); err != nil {
		return err
	}
	if err := name("clientID", r.ClientID, false); err != nil {
		return err
	}
	if err := valuesMaps("attributes", r.Attributes); err != nil {
		return err
	}
	if err := targets("targets", r.Targets); err != nil {
		return err
	}
	return valuesMaps("targets", r.TypedTargets)
}

// Validate checks the client-provided properties.
func (g ResourceGroup) Validate() error {
	if err := name("resourceGroupName", g.Name, true); err != nil {
		return err
	}
	if err := valuesMaps("attributes", g.Attributes); err != nil {
		return err
	}
	for i, c := range g.Children {
		f := fmt.Sprintf("children[%d]", i)
		if c.Type != ChildResource && c.Type != ChildResourceGroup {
			return invalid(f+".type", "must be %s or %s", ChildResource, ChildResourceGroup)
		}
		if err := id(f+".id", c.ID); err != nil {
			return err
		}
	}
	return targets("targets", g.Targets)
}
