// SPDX-License-Identifier: MIT

// Package openadr defines the domain model of OpenADR 3: programs, events,
// reports, VENs, resources and the value types they are built from.
//
// The types in this package are independent of any one protocol revision's
// JSON representation. Encoding and decoding for a specific revision (3.1.0
// today) is an internal concern of the client and server packages, so the
// structures here carry no JSON tags and are not meant to be marshalled
// directly.
//
// Entry points:
//
//   - package client: VEN and Business Logic clients for a VTN.
//   - package server: an embeddable VTN http.Handler.
//   - package auth: bearer-token validation and OAuth2 scopes.
//   - packages notification/webhook, notification/mqtt and
//     notification/websocket: the ways a VTN notifies its clients.
//   - package timeline: the intervals of an event or report as a table.
//   - package discovery: finding a VTN on the local network.
//
// ResourceGroup, MechanismWebSocket and what goes with them are not part of
// OpenADR 3.1.0: they are extensions shared with OpenLEADR, which a VTN
// has only when it is configured to.
//
// OpenADR 2.x is out of scope, as are dispatch algorithms and integrations
// with physical energy resources.
package openadr
