// SPDX-License-Identifier: MIT

package openadr

// Version identifies a revision of the OpenADR 3 specification.
type Version string

// Supported protocol revisions.
const (
	// Version310 is OpenADR 3.1.0 (document 20250807-X). The server speaks
	// it, and a client does by default.
	Version310 Version = "3.1.0"
	// Version301 is OpenADR 3.0.1, which 3.1.0 replaced without backward
	// compatibility. A client speaks it to a VTN that has not moved on.
	//
	// The domain model follows 3.1.0. A 3.0.1 client maps what the
	// revisions name differently, and refuses, with a *ValidationError,
	// what one of them cannot carry, rather than drop it:
	//
	//   - Targets are values maps in 3.0.1 (TypedTargets), strings in
	//     3.1.0 (Targets).
	//   - The program properties of 3.0.1 that 3.1.0 turned into program
	//     attributes (programLongName, retailerName, retailerLongName,
	//     programType, country, principalSubdivision, bindingEvents and
	//     localPrice) are read into, and written from, Attributes of the
	//     types 3.1.0 defines for them (PROGRAM_LONG_NAME, RETAILER_NAME,
	//     RETAILER_LONG_NAME, PROGRAM_TYPE, COUNTRY, PRINCIPAL_SUBDIVISION,
	//     BINDING_EVENTS and LOCAL). timeZoneOffset, which 3.1.0 dropped,
	//     is the attribute TIME_ZONE_OFFSET. 3.0.1 has no other program
	//     attributes.
	//   - Reports carry ProgramID, VENs and resources no ClientID, and
	//     events no Duration; report descriptors have no ReportIntervals
	//     other than INTERVALS.
	//   - Resources are reached through their VEN, /vens/{venID}/resources.
	//   - Subscriptions require a ProgramID, and notify by webhook only.
	//   - There is no GET /auth/server, GET /notifiers or MQTT.
	Version301 Version = "3.0.1"
)

// Supported reports whether this module speaks the revision v.
func (v Version) Supported() bool { return v == Version310 || v == Version301 }

// ObjectType names a kind of object addressable through the API.
type ObjectType string

// Object types defined by OpenADR 3.1.0.
const (
	ObjectProgram      ObjectType = "PROGRAM"
	ObjectEvent        ObjectType = "EVENT"
	ObjectReport       ObjectType = "REPORT"
	ObjectSubscription ObjectType = "SUBSCRIPTION"
	ObjectVEN          ObjectType = "VEN"
	ObjectResource     ObjectType = "RESOURCE"
)

// ObjectResourceGroup is the object type of a ResourceGroup, which is not
// part of OpenADR 3.1.0.
const ObjectResourceGroup ObjectType = "RESOURCE_GROUP"
