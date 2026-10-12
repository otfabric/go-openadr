// SPDX-License-Identifier: MIT

// Package v310 is the JSON representation of OpenADR 3.1.0, following the
// schemas of the specification's OpenAPI document (20250807-X).
package v310

import (
	"encoding/json"
	"time"
)

// Discriminator values of the objectType property.
const (
	typeBLVENRequest       = "BL_VEN_REQUEST"
	typeVENVENRequest      = "VEN_VEN_REQUEST"
	typeBLResourceRequest  = "BL_RESOURCE_REQUEST"
	typeVENResourceRequest = "VEN_RESOURCE_REQUEST"
	typeEventPayload       = "EVENT_PAYLOAD_DESCRIPTOR"
	typeReportPayload      = "REPORT_PAYLOAD_DESCRIPTOR"
)

// metadata is the objectMetadata schema. All of it is absent in requests,
// except objectType where a request schema uses it as a discriminator.
type metadata struct {
	ID         string     `json:"id,omitempty"`
	Created    *time.Time `json:"createdDateTime,omitempty"`
	Modified   *time.Time `json:"modificationDateTime,omitempty"`
	ObjectType string     `json:"objectType,omitempty"`
}

type program struct {
	metadata
	ProgramName         string               `json:"programName"`
	IntervalPeriod      *intervalPeriod      `json:"intervalPeriod,omitempty"`
	ProgramDescriptions []programDescription `json:"programDescriptions,omitempty"`
	PayloadDescriptors  []json.RawMessage    `json:"payloadDescriptors,omitempty"`
	Attributes          []valuesMap          `json:"attributes,omitempty"`
	Targets             []string             `json:"targets,omitempty"`
}

type programDescription struct {
	URL string `json:"URL"`
}

type event struct {
	metadata
	ProgramID          string                   `json:"programID"`
	EventName          string                   `json:"eventName,omitempty"`
	Duration           *string                  `json:"duration,omitempty"`
	Priority           *int                     `json:"priority,omitempty"`
	Targets            []string                 `json:"targets,omitempty"`
	ReportDescriptors  []reportDescriptor       `json:"reportDescriptors,omitempty"`
	PayloadDescriptors []eventPayloadDescriptor `json:"payloadDescriptors,omitempty"`
	IntervalPeriod     *intervalPeriod          `json:"intervalPeriod,omitempty"`
	Intervals          []interval               `json:"intervals,omitempty"`
}

type report struct {
	metadata
	ClientID           string                    `json:"clientID,omitempty"`
	EventID            string                    `json:"eventID"`
	ClientName         string                    `json:"clientName"`
	ReportName         string                    `json:"reportName,omitempty"`
	PayloadDescriptors []reportPayloadDescriptor `json:"payloadDescriptors,omitempty"`
	// Required, so written even when empty.
	Resources []reportResource `json:"resources"`
}

type reportResource struct {
	ResourceName   string          `json:"resourceName"`
	IntervalPeriod *intervalPeriod `json:"intervalPeriod,omitempty"`
	Intervals      []interval      `json:"intervals"`
}

type ven struct {
	metadata
	ClientID   string      `json:"clientID,omitempty"`
	VenName    string      `json:"venName"`
	Attributes []valuesMap `json:"attributes,omitempty"`
	Targets    []string    `json:"targets,omitempty"`
}

type resource struct {
	metadata
	ClientID     string      `json:"clientID,omitempty"`
	ResourceName string      `json:"resourceName"`
	VenID        string      `json:"venID"`
	Attributes   []valuesMap `json:"attributes,omitempty"`
	Targets      []string    `json:"targets,omitempty"`
}

type interval struct {
	// A pointer so that a missing id can be told from id 0.
	ID             *int32          `json:"id"`
	IntervalPeriod *intervalPeriod `json:"intervalPeriod,omitempty"`
	Payloads       []valuesMap     `json:"payloads"`
}

type intervalPeriod struct {
	Start          *time.Time `json:"start,omitempty"`
	Duration       *string    `json:"duration,omitempty"`
	RandomizeStart *string    `json:"randomizeStart,omitempty"`
}

type valuesMap struct {
	Type   string            `json:"type"`
	Values []json.RawMessage `json:"values"`
}

type point struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

type eventPayloadDescriptor struct {
	ObjectType  string `json:"objectType"`
	PayloadType string `json:"payloadType"`
	Units       string `json:"units,omitempty"`
	Currency    string `json:"currency,omitempty"`
}

type reportPayloadDescriptor struct {
	ObjectType  string   `json:"objectType"`
	PayloadType string   `json:"payloadType"`
	ReadingType string   `json:"readingType,omitempty"`
	Units       string   `json:"units,omitempty"`
	Accuracy    *float64 `json:"accuracy,omitempty"`
	Confidence  *int     `json:"confidence,omitempty"`
}

// reportDescriptor uses pointers for the properties whose schema default is
// not the Go zero value, so that an absent property decodes to its default.
type reportDescriptor struct {
	PayloadType     string   `json:"payloadType"`
	ReadingType     string   `json:"readingType,omitempty"`
	Units           string   `json:"units,omitempty"`
	Targets         []string `json:"targets,omitempty"`
	Aggregate       bool     `json:"aggregate"`
	StartInterval   *int32   `json:"startInterval"`
	NumIntervals    *int32   `json:"numIntervals"`
	Historical      *bool    `json:"historical"`
	Frequency       *int32   `json:"frequency"`
	Repeat          *int32   `json:"repeat"`
	ReportIntervals *string  `json:"reportIntervals"`
}

type problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

type authServerInfo struct {
	TokenURL string `json:"tokenURL"`
}
