// SPDX-License-Identifier: MIT

// Package e2e holds end-to-end tests of go-openadr against itself: the
// clients of this module against the VTN of this module, over real HTTPS
// connections on loopback, with the token service, the stores, the webhook
// sender and handler, and the MQTT publisher and subscriber that the module
// ships.
//
// Where package interop shows that each side agrees with other
// implementations, this package shows that the two sides agree with each
// other, in every feature, including those no other implementation can
// exercise yet. A bug that both sides share stays invisible here; that is
// what interop and the conformance suite of otfabric/openadr-interop are
// for.
package e2e
