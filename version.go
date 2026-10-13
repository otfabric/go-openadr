// SPDX-License-Identifier: MIT

package openadr

// Version identifies a revision of the OpenADR 3 specification.
type Version string

// Supported protocol revisions.
const (
	// Version310 is OpenADR 3.1.0 (document 20250807-X).
	Version310 Version = "3.1.0"
)

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
