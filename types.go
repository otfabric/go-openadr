// SPDX-License-Identifier: MIT

package openadr

import "time"

// Metadata is assigned by the VTN when an object is created or modified. A
// VTN ignores these fields in create and update requests, so callers leave
// them zero when building an object to send.
type Metadata struct {
	ID       string
	Created  time.Time
	Modified time.Time
}

// Program is a demand-response program or tariff.
type Program struct {
	Metadata

	// Name identifies the program and is unique within a VTN.
	Name string
	// IntervalPeriod is the temporal span of the program.
	IntervalPeriod *IntervalPeriod
	// Descriptions link to human or machine readable descriptions.
	Descriptions []ProgramDescription
	// PayloadDescriptors give context to the payloads of the program's
	// events and reports. Each element is an EventPayloadDescriptor or a
	// ReportPayloadDescriptor.
	PayloadDescriptors []PayloadDescriptor
	Attributes         []ValuesMap
	// Targets restrict which VENs may read the program. A VTN only shows a
	// VEN the targets that VEN asked for.
	Targets []string
}

// ProgramDescription links to a description of a program.
type ProgramDescription struct {
	URL string
}

// Event communicates a demand-response request to VENs.
type Event struct {
	Metadata

	// ProgramID is the program the event belongs to.
	ProgramID string
	Name      string
	// Duration optionally bounds the event; it may be used to loop intervals.
	Duration *Duration
	// Priority orders events; a lower number is a higher priority.
	Priority           *int
	Targets            []string
	ReportDescriptors  []ReportDescriptor
	PayloadDescriptors []EventPayloadDescriptor
	// IntervalPeriod sets the default start and duration of Intervals.
	IntervalPeriod *IntervalPeriod
	Intervals      []Interval
}

// Report carries data from a VEN in response to an event.
type Report struct {
	Metadata

	// ClientID is written by the VTN from the creating client's token.
	ClientID string
	// EventID is the event the report is associated with.
	EventID string
	// ClientName is chosen by the reporting client.
	ClientName         string
	Name               string
	PayloadDescriptors []ReportPayloadDescriptor
	Resources          []ReportResource
}

// ReportResource is the report data of one resource. A ResourceName of
// AggregatedReport denotes data aggregated over several resources.
type ReportResource struct {
	ResourceName   string
	IntervalPeriod *IntervalPeriod
	Intervals      []Interval
}

// AggregatedReport is the resource name of an aggregated report.
const AggregatedReport = "AGGREGATED_REPORT"

// VEN is a virtual end node known to the VTN.
type VEN struct {
	Metadata

	// ClientID ties the VEN to the client credentials it authenticates with.
	// Business Logic supplies it; for a VEN-created object the VTN writes it
	// from the token.
	ClientID string
	// Name is unique within a VTN.
	Name       string
	Attributes []ValuesMap
	// Targets are granted by Business Logic and decide which targeted
	// programs and events the VEN may read. Only Business Logic may set them.
	Targets []string
}

// Resource is a resource of a VEN, such as a device or a group of devices.
type Resource struct {
	Metadata

	ClientID string
	// VENID is the VEN the resource belongs to.
	VENID string
	// Name is unique within its VEN.
	Name       string
	Attributes []ValuesMap
	// Targets are granted by Business Logic; see VEN.Targets.
	Targets []string
}

// Interval is a temporal window with payloads. ID is chosen by the client
// that creates the interval and is not a sequence number.
type Interval struct {
	ID             int32
	IntervalPeriod *IntervalPeriod
	Payloads       []ValuesMap
}

// IntervalPeriod defines the temporal aspects of one or more intervals.
//
// The specification gives two values a special meaning: a Start in year 1
// may mean "now", and a Duration of P9999Y may mean "indefinitely"; see
// Duration.IsInfinite.
type IntervalPeriod struct {
	Start    *time.Time
	Duration *Duration
	// RandomizeStart is the range of a client-applied offset to Start.
	RandomizeStart *Duration
}

// ValuesMap is a list of values together with a type that says what they
// are, such as "PRICE". Type is one of the strings enumerated in the
// specification or a privately defined one; this package does not restrict it.
type ValuesMap struct {
	Type   string
	Values []Value
}

// PayloadDescriptor is an EventPayloadDescriptor or a ReportPayloadDescriptor.
type PayloadDescriptor interface {
	payloadDescriptor()
}

// EventPayloadDescriptor gives context, such as units and currency, to event
// payloads of one type.
type EventPayloadDescriptor struct {
	PayloadType string
	Units       string
	Currency    string
}

// ReportPayloadDescriptor gives context, such as units and data quality, to
// report payloads of one type.
type ReportPayloadDescriptor struct {
	PayloadType string
	ReadingType string
	Units       string
	Accuracy    *float64
	// Confidence is a percentage, 0 to 100.
	Confidence *int
}

func (EventPayloadDescriptor) payloadDescriptor()  {}
func (ReportPayloadDescriptor) payloadDescriptor() {}

// ReportIntervals says which intervals a requested report covers.
type ReportIntervals string

// Report interval options defined by OpenADR 3.1.0.
const (
	ReportIntervalsIntervals    ReportIntervals = "INTERVALS"
	ReportIntervalsSubIntervals ReportIntervals = "SUB_INTERVALS"
	ReportIntervalsOpen         ReportIntervals = "OPEN_INTERVALS"
)

// ReportDescriptor requests a report from a VEN. NewReportDescriptor returns
// one holding the defaults of the specification; the zero value does not.
type ReportDescriptor struct {
	PayloadType string
	ReadingType string
	Units       string
	Targets     []string
	// Aggregate asks for one result over all targeted resources.
	Aggregate bool
	// StartInterval is the interval on which to report; -1 is the end of
	// the last interval.
	StartInterval int32
	// NumIntervals is the number of intervals to include; -1 is all.
	NumIntervals int32
	// Historical reports on the intervals before StartInterval, as opposed
	// to those after it.
	Historical bool
	// Frequency is the number of intervals between reports; -1 is the same
	// as NumIntervals.
	Frequency int32
	// Repeat is the number of reports to generate; -1 is indefinitely.
	Repeat          int32
	ReportIntervals ReportIntervals
}

// NewReportDescriptor returns a descriptor for payloadType with every other
// field at the default the specification defines for it.
func NewReportDescriptor(payloadType string) ReportDescriptor {
	return ReportDescriptor{
		PayloadType:     payloadType,
		StartInterval:   -1,
		NumIntervals:    -1,
		Historical:      true,
		Frequency:       -1,
		Repeat:          1,
		ReportIntervals: ReportIntervalsIntervals,
	}
}
