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

type subscription struct {
	metadata
	ClientID   string `json:"clientID,omitempty"`
	ClientName string `json:"clientName"`
	ProgramID  string `json:"programID,omitempty"`
	// Required, so written even when empty.
	ObjectOperations []objectOperation `json:"objectOperations"`
	Targets          []string          `json:"targets,omitempty"`
}

type objectOperation struct {
	Objects    []string `json:"objects"`
	Operations []string `json:"operations"`
	// Not of 3.1.0, and absent for a webhook, which is what 3.1.0 has.
	Mechanism string `json:"mechanism,omitempty"`
	// Required by 3.1.0, where every subscription is to a webhook; absent
	// for another mechanism.
	CallbackURL string `json:"callbackUrl,omitempty"`
	BearerToken string `json:"bearerToken,omitempty"`
}

type notification struct {
	// Not of 3.1.0: read when a VTN sends it, never written.
	ID         string          `json:"id,omitempty"`
	ObjectType string          `json:"objectType"`
	Operation  string          `json:"operation"`
	Object     json.RawMessage `json:"object"`
	Targets    []string        `json:"targets,omitempty"`
}

type notifiers struct {
	Webhook bool `json:"WEBHOOK"`
	// Not of 3.1.0, and written only by a VTN that has it.
	WebSocket bool         `json:"WEBSOCKET,omitempty"`
	MQTT      *mqttBinding `json:"MQTT,omitempty"`
}

type mqttBinding struct {
	URIs           []string           `json:"URIS"`
	Serialization  string             `json:"serialization"`
	Authentication mqttAuthentication `json:"authentication"`
}

type mqttAuthentication struct {
	Method     string `json:"method"`
	Username   string `json:"username,omitempty"`
	CACert     string `json:"caCert,omitempty"`
	ClientCert string `json:"clientCert,omitempty"`
	ClientKey  string `json:"clientKey,omitempty"`
}

type topicsResponse struct {
	Topics topics `json:"topics"`
}

// topics has UPDATE and DELETE required by the schema; CREATE and ALL are
// absent where the VTN has no such topic.
type topics struct {
	Create string `json:"CREATE,omitempty"`
	Update string `json:"UPDATE"`
	Delete string `json:"DELETE"`
	All    string `json:"ALL,omitempty"`
}

type interval struct {
	// A pointer so that a missing id can be told from id 0.
	ID             *int32          `json:"id"`
	IntervalPeriod *intervalPeriod `json:"intervalPeriod,omitempty"`
	Payloads       []valuesMap     `json:"payloads"`
}

// startTime is the start of an interval period. The schema makes it a
// date-time, and the User Guide writes "the beginning of time", which means
// "now" or "after the previous interval", in two forms that are not one:
// 0001-01-01 and 0001-01-01T00:00:00. Both are read as the zero time.
type startTime time.Time

func (t startTime) MarshalJSON() ([]byte, error) { return time.Time(t).MarshalJSON() }

func (t *startTime) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case `"0001-01-01"`, `"0001-01-01T00:00:00"`:
		*t = startTime(time.Time{})
		return nil
	}
	return (*time.Time)(t).UnmarshalJSON(data)
}

type intervalPeriod struct {
	Start          *startTime `json:"start,omitempty"`
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
