//go:build interop

// SPDX-License-Identifier: MIT

// This commit against openadr-interop-otfabric, the image of an earlier
// go-openadr release (own/go-openadr of otfabric/openadr-interop), in both
// roles: this module's clients against the VTN of the image, and the clients
// of the image against this module's VTN.
//
//	OPENADR_INTEROP_OTFABRIC_IMAGE   the image; these tests skip when it is unset
//
//	OPENADR_INTEROP_OTFABRIC_IMAGE=ghcr.io/otfabric/openadr-interop-otfabric:latest \
//	    go test -tags=interop -run Otfabric ./interop/
//
// The e2e tests run both sides from one commit; these find a change on the
// wire that breaks an earlier release. A pass is not independent
// verification: see INTEROP.md. The image is no reference, so it has no
// default and OPENADR_INTEROP_REQUIRE does not make it required.

package interop

import (
	"os"
	"testing"
)

const otfabricEnv = "OPENADR_INTEROP_OTFABRIC_IMAGE"

// otfabric is go-openadr itself, as published by otfabric/openadr-interop.
// It runs only when OPENADR_INTEROP_OTFABRIC_IMAGE names an image.
const otfabric = "otfabric"

func requireOtfabric(t *testing.T) {
	t.Helper()
	if os.Getenv(otfabricEnv) == "" {
		t.Skip(otfabricEnv + " is not set")
	}
}

// This module's clients against the VTN of the image.

func TestOtfabricVTNWorkflow(t *testing.T) {
	requireOtfabric(t)
	testVTNWorkflow(t, vtnPeer(t, otfabric))
}

func TestOtfabricVTNErrors(t *testing.T) {
	requireOtfabric(t)
	testVTNErrors(t, vtnPeer(t, otfabric))
}

func TestOtfabricVTNVENsResourcesAndTargets(t *testing.T) {
	requireOtfabric(t)
	testVTNVENsResourcesAndTargets(t, vtnPeer(t, otfabric))
}

// The clients of the image against this module's VTN.

func TestOtfabricClientReadsGoVTN(t *testing.T) {
	requireOtfabric(t)
	testClientReadsGoVTN(t, otfabric)
}

func TestOtfabricClientWorkflowOnGoVTN(t *testing.T) {
	requireOtfabric(t)
	testClientWorkflowOnGoVTN(t, otfabric)
}

// Webhooks: the VTN of the image notifies this module's handler, and this
// module's VTN the receiver of the image's listen-webhook.

func TestOtfabricVTNWebhook(t *testing.T) {
	requireOtfabric(t)
	testVTNWebhook(t, otfabric)
}

func TestOtfabricListensToGoVTNWebhook(t *testing.T) {
	requireOtfabric(t)
	testListenWebhookOnGoVTN(t, otfabric)
}
