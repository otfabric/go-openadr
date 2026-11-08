// SPDX-License-Identifier: MIT

package transcript

import (
	"regexp"
	"strings"
	"sync"
)

// used counts the entries of the tables that matched, for UnusedKnown.
var used sync.Map

// Classes of a known difference between a recording and what this module
// makes of it (testdata/transcripts/README.md, "Differences").
const (
	ClassDecoder      = "decoder defect"
	ClassEncoder      = "encoder defect"
	ClassStateMachine = "state-machine defect"
	ClassMissing      = "missing supported behaviour"
	ClassVendor       = "implementation-specific difference"
	ClassVariation    = "legitimate protocol variation"
	ClassUnsupported  = "unsupported feature"
	ClassIncomplete   = "evidence incomplete"
)

// knownDiff is one entry of the table: a source ("*" for any), the shape
// of the body, the kind of difference and a pattern of its JSON pointer
// (array indexes as *), and why.
type knownDiff struct {
	source, shape, kind, path string
	// want, when not empty, is the recorded value the entry is about.
	want       string
	class, why string
}

var arrayIndex = regexp.MustCompile(`/[0-9]+(/|$)`)

// PathPattern generalises the array indexes of a JSON pointer to *.
func PathPattern(p string) string {
	for arrayIndex.MatchString(p) {
		p = arrayIndex.ReplaceAllString(p, "/*$1")
	}
	return p
}

// Known reports whether a difference between a recorded body and this
// module's encoding of what it decoded from it is classified, and how.
func Known(source, shape string, d Diff) (string, bool) {
	p := PathPattern(d.Path)
	for _, k := range knownDiffs {
		if (k.source == "*" || k.source == source) && (k.shape == "*" || k.shape == shape) && k.kind == d.Kind && match(k.path, p) &&
			(k.want == "" || k.want == d.Want) {
			used.Store(k, true)
			return k.class + ": " + k.why, true
		}
	}
	return "", false
}

// match matches a pattern whose "**" stands for any rest of the path.
func match(pattern, p string) bool {
	if rest, ok := strings.CutSuffix(pattern, "**"); ok {
		return strings.HasPrefix(p, rest)
	}
	return pattern == p
}

// knownRefusal is a 2xx answer this module's client refuses.
type knownRefusal struct {
	source, route, err string
	class, why         string
}

// KnownRefusal reports whether the client's refusal of a recorded 2xx
// answer is classified.
func KnownRefusal(source, route string, err error) (string, bool) {
	for _, k := range knownRefusals {
		if (k.source == "*" || k.source == source) && k.route == route && strings.Contains(err.Error(), k.err) {
			used.Store(k, true)
			return k.class + ": " + k.why, true
		}
	}
	return "", false
}

// Why a value is read as absent, or written though it was not sent.
const (
	whyNull = "a member that is null is read as absent: every optional member the request and object schemas give a default has null as that default; " +
		"this module writes an absent member by leaving it out (OpenADR 3.1.0 and 3.0.1 openadr3.yaml, components.schemas)"
	whyEmptyTargets       = "an empty targets array is read as no targets, and no targets are written by leaving targets out"
	whyDescriptorDefaults = "the members of a report descriptor that were absent are written with their schema defaults " +
		"(aggregate false, startInterval -1, numIntervals -1, historical true, frequency -1, repeat 1, reportIntervals INTERVALS in 3.1.0)"
	whyUnknownMember     = "a member the schema does not define is ignored, not kept (LIMITATIONS.md)"
	whyMechanism         = "mechanism is not of OpenADR 3.1.0 (an extension shared with OpenLEADR); WEBHOOK, what 3.1.0 has, is written by leaving it out"
	whyRequestObjectType = "OpenADR 3.0.1 has no request schemas: a ven or resource sent with objectType is read, and a request is written without it, " +
		"as this module's 3.0.1 clients send it and OpenLEADR v0.1.4 accepts it"
)

var knownDiffs = []knownDiff{
	// Encoding choices of this module, for any peer.
	{source: "*", shape: "*", kind: DiffMissing, path: "**", want: "null", class: ClassVariation, why: whyNull},
	{source: "*", shape: "*", kind: DiffMissing, path: "/targets", want: "[]", class: ClassVariation, why: whyEmptyTargets},
	{source: "*", shape: "*", kind: DiffMissing, path: "/*/targets", want: "[]", class: ClassVariation, why: whyEmptyTargets},
	{source: "*", shape: "*", kind: DiffMissing, path: "/object/targets", want: "[]", class: ClassVariation, why: whyEmptyTargets},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/reportDescriptors/*/aggregate", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/reportDescriptors/*/startInterval", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/reportDescriptors/*/numIntervals", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/reportDescriptors/*/historical", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/reportDescriptors/*/frequency", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/reportDescriptors/*/repeat", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/reportDescriptors/*/reportIntervals", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/*/reportDescriptors/*/aggregate", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/*/reportDescriptors/*/startInterval", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/*/reportDescriptors/*/numIntervals", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/*/reportDescriptors/*/historical", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/*/reportDescriptors/*/frequency", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/*/reportDescriptors/*/repeat", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeEvent, kind: DiffExtra, path: "/*/reportDescriptors/*/reportIntervals", class: ClassVariation, why: whyDescriptorDefaults},
	{source: "*", shape: ShapeProgram, kind: DiffMissing, path: "/noSuchMember", class: ClassVariation, why: whyUnknownMember},
	{source: "elaadnl", shape: ShapeResourceGroup, kind: DiffMissing, path: "/objectType", want: `"RESOURCE_GROUP"`, class: ClassVariation,
		why: "resource groups are not of OpenADR 3.1.0; this module writes their requests without objectType, as OpenLEADR v0.2.8 does, and reads one with it"},
	{source: "openleadr301", shape: ShapeVEN, kind: DiffMissing, path: "/objectType", class: ClassVariation, why: whyRequestObjectType},
	{source: "openleadr301", shape: ShapeResource, kind: DiffMissing, path: "/objectType", class: ClassVariation, why: whyRequestObjectType},

	// OpenLEADR v0.2.8.
	{source: "openleadr", shape: ShapeVEN, kind: DiffExtra, path: "/objectType", class: ClassVendor,
		why: "OpenLEADR v0.2.8 sends a ven without objectType, which objectMetadata has; this module writes VEN"},
	{source: "openleadr", shape: ShapeVEN, kind: DiffExtra, path: "/*/objectType", class: ClassVendor,
		why: "OpenLEADR v0.2.8 sends a ven without objectType, which objectMetadata has; this module writes VEN"},
	{source: "openleadr", shape: ShapeSubscription, kind: DiffExtra, path: "/objectType", class: ClassVendor,
		why: "OpenLEADR v0.2.8 sends a subscription without objectType; this module writes SUBSCRIPTION"},
	{source: "openleadr", shape: ShapeSubscription, kind: DiffExtra, path: "/*/objectType", class: ClassVendor,
		why: "OpenLEADR v0.2.8 sends a subscription without objectType; this module writes SUBSCRIPTION"},
	{source: "openleadr", shape: ShapeSubscription, kind: DiffMissing, path: "/clientId", class: ClassVendor,
		why: "OpenLEADR v0.2.8 names the member clientId; encoding/json matches member names without regard to case, so this module reads it as clientID and writes clientID"},
	{source: "openleadr", shape: ShapeSubscription, kind: DiffMissing, path: "/*/clientId", class: ClassVendor,
		why: "OpenLEADR v0.2.8 names the member clientId; encoding/json matches member names without regard to case, so this module reads it as clientID and writes clientID"},
	{source: "openleadr", shape: ShapeSubscription, kind: DiffExtra, path: "/clientID", class: ClassVendor, why: "see clientId"},
	{source: "openleadr", shape: ShapeSubscription, kind: DiffExtra, path: "/*/clientID", class: ClassVendor, why: "see clientId"},
	{source: "openleadr", shape: ShapeSubscription, kind: DiffMissing, path: "/objectOperations/*/mechanism", want: `"WEBHOOK"`, class: ClassVariation, why: whyMechanism},
	{source: "openleadr", shape: ShapeSubscription, kind: DiffMissing, path: "/*/objectOperations/*/mechanism", want: `"WEBHOOK"`, class: ClassVariation, why: whyMechanism},
	{source: "openleadr", shape: "*", kind: DiffExtra, path: "/payloadDescriptors/*/objectType", class: ClassVendor,
		why: "OpenLEADR v0.2.8 answers payload descriptors without their objectType; this module reads them by the object they are in and writes the objectType"},
	{source: "openleadr", shape: "*", kind: DiffExtra, path: "/*/payloadDescriptors/*/objectType", class: ClassVendor,
		why: "OpenLEADR v0.2.8 answers payload descriptors without their objectType; this module reads them by the object they are in and writes the objectType"},
	{source: "openleadr", shape: ShapeNotifiers, kind: DiffExtra, path: "/WEBHOOK", class: ClassVendor,
		why: "OpenLEADR v0.2.8 leaves out WEBHOOK, which notifiersResponse requires; this module reads its absence as false and writes it"},
	{source: "openleadr", shape: ShapeNotifiers, kind: DiffMissing, path: "/PUSH-MQTT", class: ClassUnsupported,
		why: "PUSH-MQTT is OpenLEADR's own binding, not of OpenADR 3.1.0; it is ignored"},
	{source: "openleadr", shape: ShapeNotification, kind: DiffMissing, path: "/id", class: ClassVendor,
		why: "OpenLEADR v0.2.8 gives an MQTT notification an id, which the notification schema does not have; it is read into Notification.ID, " +
			"and not written again: this module's VTN gives its notifications none"},

	// The cases of the specification.
	{source: "spec-3.1.0", shape: ShapeNotification, kind: DiffExtra, path: "/object/**", class: ClassIncomplete, why: whySpecNotification},
	{source: "spec-3.0.1", shape: ShapeVEN, kind: DiffMissing, path: "/resources", class: ClassUnsupported,
		why: "the resources a 3.0.1 ven object carries inline are not read: a VEN's resources are asked for with their own operation (internal/wire/v301)"},

	// OpenLEADR v0.1.4 (OpenADR 3.0.1).
	{source: "openleadr301", shape: ShapeVEN, kind: DiffMissing, path: "/*/resources", class: ClassUnsupported,
		why: "the resources a 3.0.1 ven object carries inline are not read: a VEN's resources are asked for with their own operation (internal/wire/v301)"},
}

// whySpecNotification: the example the documents give for notification.object.
const whySpecNotification = "the documents' example of notification.object is {}, which none of the object schemas it may be accepts " +
	"(an event needs programID); the 3.1.0 codec reads it leniently as an event and writes the metadata and programID it lacks, the 3.0.1 codec refuses it"

var knownRefusals = []knownRefusal{
	{source: "spec-3.1.0", route: "authServerInfo", err: "invalid tokenURL: is required", class: ClassIncomplete,
		why: "the document gives no example, default or enum for authServerInfo.tokenURL, so the case has none, and tokenURL is required"},
	{source: "spec-3.0.1", route: "notification", err: "invalid intervals: is required", class: ClassIncomplete, why: whySpecNotification},
}

// UnusedKnown lists the entries of the tables that have not matched a
// difference since the program started: an entry that matches nothing
// any more is to be removed, so that the tables say what is.
func UnusedKnown() []string {
	var out []string
	for _, k := range knownDiffs {
		if _, ok := used.Load(k); !ok {
			out = append(out, k.source+" "+k.shape+" "+k.kind+" "+k.path)
		}
	}
	for _, k := range knownRefusals {
		if _, ok := used.Load(k); !ok {
			out = append(out, k.source+" "+k.route+" "+k.err)
		}
	}
	return out
}
