// SPDX-License-Identifier: MIT

// Package v310 is the JSON representation of OpenADR 3.1.0, following the
// schemas of the specification's OpenAPI document (20250807-X).
package v310

import (
	"encoding/json"

	"github.com/otfabric/go-openadr/internal/wire/common"
)

// Discriminator values of the objectType property.
const (
	typeBLVENRequest       = "BL_VEN_REQUEST"
	typeVENVENRequest      = "VEN_VEN_REQUEST"
	typeBLResourceRequest  = "BL_RESOURCE_REQUEST"
	typeVENResourceRequest = "VEN_RESOURCE_REQUEST"
)

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

// reportDescriptor is the reportDescriptor schema: what both revisions
// have, targets as strings, and reportIntervals, which 3.1.0 added.
type reportDescriptor struct {
	common.ReportDescriptorCommon
	Targets         []string `json:"targets,omitempty"`
	ReportIntervals *string  `json:"reportIntervals"`
}

type authServerInfo struct {
	TokenURL string `json:"tokenURL"`
}
