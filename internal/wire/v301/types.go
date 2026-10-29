// SPDX-License-Identifier: MIT

// Package v301 is the JSON representation of OpenADR 3.0.1, following the
// schemas of the specification's OpenAPI document (oadr3-org/specification
// a35682b, 2024-11-26).
//
// 3.0.1 is spoken by the client only. Where 3.0.1 and 3.1.0 differ, the
// domain model follows 3.1.0, and this package maps or refuses what 3.0.1
// writes otherwise: see openadr.Version301.
package v301

import (
	"encoding/json"

	"github.com/otfabric/go-openadr/internal/wire/common"
)

type (
	metadata       = common.Metadata
	intervalPeriod = common.IntervalPeriod
	valuesMap      = common.ValuesMap
	interval       = common.Interval
)

type program struct {
	metadata
	ProgramName          string               `json:"programName"`
	ProgramLongName      string               `json:"programLongName,omitempty"`
	RetailerName         string               `json:"retailerName,omitempty"`
	RetailerLongName     string               `json:"retailerLongName,omitempty"`
	ProgramType          string               `json:"programType,omitempty"`
	Country              string               `json:"country,omitempty"`
	PrincipalSubdivision string               `json:"principalSubdivision,omitempty"`
	TimeZoneOffset       *string              `json:"timeZoneOffset,omitempty"`
	IntervalPeriod       *intervalPeriod      `json:"intervalPeriod,omitempty"`
	ProgramDescriptions  []programDescription `json:"programDescriptions,omitempty"`
	BindingEvents        *bool                `json:"bindingEvents,omitempty"`
	LocalPrice           *bool                `json:"localPrice,omitempty"`
	PayloadDescriptors   []json.RawMessage    `json:"payloadDescriptors,omitempty"`
	Targets              []valuesMap          `json:"targets,omitempty"`
}

type programDescription struct {
	URL string `json:"URL"`
}

type event struct {
	metadata
	ProgramID          string                          `json:"programID"`
	EventName          string                          `json:"eventName,omitempty"`
	Priority           *int                            `json:"priority,omitempty"`
	Targets            []valuesMap                     `json:"targets,omitempty"`
	ReportDescriptors  []reportDescriptor              `json:"reportDescriptors,omitempty"`
	PayloadDescriptors []common.EventPayloadDescriptor `json:"payloadDescriptors,omitempty"`
	IntervalPeriod     *intervalPeriod                 `json:"intervalPeriod,omitempty"`
	// Required, so written even when empty.
	Intervals []interval `json:"intervals"`
}

type reportDescriptor struct {
	common.ReportDescriptorCommon
	Targets []valuesMap `json:"targets,omitempty"`
}

type report struct {
	metadata
	ProgramID          string                           `json:"programID"`
	EventID            string                           `json:"eventID"`
	ClientName         string                           `json:"clientName"`
	ReportName         string                           `json:"reportName,omitempty"`
	PayloadDescriptors []common.ReportPayloadDescriptor `json:"payloadDescriptors,omitempty"`
	// Required, so written even when empty.
	Resources []common.ReportResource `json:"resources"`
}

// ven has resources too in 3.0.1, the VEN's resources inlined. They are
// not read: the resources of a VEN are asked for with their own operation.
type ven struct {
	metadata
	VenName    string      `json:"venName"`
	Attributes []valuesMap `json:"attributes,omitempty"`
	Targets    []valuesMap `json:"targets,omitempty"`
}

type resource struct {
	metadata
	ResourceName string      `json:"resourceName"`
	VenID        string      `json:"venID,omitempty"`
	Attributes   []valuesMap `json:"attributes,omitempty"`
	Targets      []valuesMap `json:"targets,omitempty"`
}

type subscription struct {
	metadata
	ClientName string `json:"clientName"`
	ProgramID  string `json:"programID"`
	// Required, so written even when empty.
	ObjectOperations []objectOperation `json:"objectOperations"`
	Targets          []valuesMap       `json:"targets,omitempty"`
}

type objectOperation struct {
	Objects     []string `json:"objects"`
	Operations  []string `json:"operations"`
	CallbackURL string   `json:"callbackUrl"`
	BearerToken string   `json:"bearerToken,omitempty"`
}

type notification struct {
	ObjectType string          `json:"objectType"`
	Operation  string          `json:"operation"`
	Object     json.RawMessage `json:"object"`
	Targets    []valuesMap     `json:"targets,omitempty"`
}
