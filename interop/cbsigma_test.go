//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
)

// cbsigma is cB-Sigma openadr (Go, Apache-2.0), an implementation of
// OpenADR 3 independent of this module, as openadr-interop v0.8.0 publishes
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
// and OpenLEADR's answer 200. cB-Sigma openadr v0.2.0 answers 404:
// https://github.com/cB-Sigma/openadr/issues/1. The test asserts the bug
// for that version, and the fixed behaviour for any other, so that a fixed
// image tells.
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
	_, err = p.ven.Program(c, flex)
	if upstreamVersion(t, image(cbsigma)) == "v0.2.0" {
		if !errors.Is(err, openadr.ErrNotFound) {
			t.Fatalf("cB-Sigma/openadr#1 no longer shows (err %v): remove the waiver of this test", err)
		}
		t.Logf("known bug cB-Sigma/openadr#1: ven-1 reads group-a-flex by ID: %v", err)
		return
	}
	if err != nil {
		t.Errorf("ven-1, granted group-a, reads group-a-flex by ID: %v", err)
	}
}

// The cbsigma clients against this module's VTN.

func TestCbsigmaReadsGoVTN(t *testing.T) { testClientReadsGoVTN(t, cbsigma) }

func TestCbsigmaWorkflowOnGoVTN(t *testing.T) { testClientWorkflowOnGoVTN(t, cbsigma) }

// This module's webhook.Sender notifies the receiver of listen-webhook.
func TestCbsigmaListensToGoVTNWebhook(t *testing.T) { testListenWebhookOnGoVTN(t, cbsigma) }

// upstreamVersion is the version of the wrapped implementation an image
// reports.
func upstreamVersion(t *testing.T, img string) string {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, exit, err := docker(c, "run", "--rm", img, "print-capabilities")
	if err != nil || exit != 0 {
		t.Fatalf("print-capabilities: exit %d, %v", exit, err)
	}
	var caps struct {
		Upstream struct {
			Version string `json:"version"`
		} `json:"upstream"`
	}
	if err := json.Unmarshal(out, &caps); err != nil {
		t.Fatal(err)
	}
	return caps.Upstream.Version
}
