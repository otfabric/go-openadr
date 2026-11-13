//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"testing"

	"github.com/otfabric/go-openadr/client"
)

// cbsigma is cB-Sigma openadr (Go, Apache-2.0), an implementation of
// OpenADR 3 independent of this module, as openadr-interop v0.8.2 publishes
// it: its VTN (oadr3/vtn) and its REST client (oadr3.APIClient) for both
// roles. See INTEROP.md.
const cbsigma = "cbsigma"

// This module's clients against the cbsigma VTN.

func TestCbsigmaVTNWorkflow(t *testing.T) { testVTNWorkflow(t, vtnPeer(t, cbsigma)) }

func TestCbsigmaVTNErrors(t *testing.T) { testVTNErrors(t, vtnPeer(t, cbsigma)) }

func TestCbsigmaVTNVENsResourcesAndTargets(t *testing.T) {
	testVTNVENsResourcesAndTargets(t, vtnPeer(t, cbsigma))
}

// The cbsigma VTN notifies this module's webhook handler.
func TestCbsigmaVTNWebhook(t *testing.T) { testVTNWebhook(t, cbsigma) }

// A VEN reads a program it is targeted by, by ID. GET /programs/{programID}
// has no query parameter, so it cannot name the target; this module's VTN
// and OpenLEADR's answer 200. cB-Sigma openadr v0.2.0 answered 404
// (https://github.com/cB-Sigma/openadr/issues/1), fixed in v0.2.1.
func TestCbsigmaVTNReadsTargetedProgramByID(t *testing.T) {
	p := vtnPeer(t, cbsigma)
	c := ctx(t)
	list, err := p.bl.Programs(c, client.ProgramQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var flex string
	for _, pr := range list {
		if pr.Name == "group-a-flex" {
			flex = pr.ID
		}
	}
	if flex == "" {
		t.Fatal("no program group-a-flex")
	}
	if _, err := p.ven.Program(c, flex); err != nil {
		t.Errorf("ven-1, granted group-a, reads group-a-flex by ID: %v", err)
	}
}

// The cbsigma clients against this module's VTN.

func TestCbsigmaReadsGoVTN(t *testing.T) { testClientReadsGoVTN(t, cbsigma) }

func TestCbsigmaWorkflowOnGoVTN(t *testing.T) { testClientWorkflowOnGoVTN(t, cbsigma) }

// This module's webhook.Sender notifies the receiver of listen-webhook.
func TestCbsigmaListensToGoVTNWebhook(t *testing.T) { testListenWebhookOnGoVTN(t, cbsigma) }
