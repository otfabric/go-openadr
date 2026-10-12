// SPDX-License-Identifier: MIT

// Package vtntest starts an in-process VTN for the tests of this module.
package vtntest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// VTN is a running test VTN with one Business Logic client and two VENs.
type VTN struct {
	URL   string
	Store *storage.Memory
	BL    *client.BL
	// VEN1 and VEN2 authenticate as the clients "ven-1" and "ven-2".
	VEN1, VEN2 *client.VEN
}

// Start runs a VTN that is shut down when the test ends.
func Start(t testing.TB) *VTN {
	t.Helper()
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: []auth.Client{
		{ID: "bl", Secret: "bl-secret", Scopes: auth.BusinessLogicScopes()},
		{ID: "ven-1", Secret: "ven-1-secret", Scopes: auth.VENScopes()},
		{ID: "ven-2", Secret: "ven-2-secret", Scopes: auth.VENScopes()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewMemory()
	// Serve under a prefix, as deployments do. The VTN announces its token
	// endpoint by URL, which is known once the listener is.
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	vtn, err := server.New(server.Config{Store: store, Validator: issuer, TokenURL: ts.URL + "/openadr3/auth/token", TokenHandler: issuer})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/openadr3/", http.StripPrefix("/openadr3", vtn))

	v := &VTN{URL: ts.URL + "/openadr3", Store: store}
	cfg := func(id string) client.Config {
		return client.Config{
			BaseURL:     v.URL,
			HTTPClient:  ts.Client(),
			Credentials: &client.Credentials{ClientID: id, ClientSecret: id + "-secret"},
		}
	}
	if v.BL, err = client.NewBL(cfg("bl")); err != nil {
		t.Fatal(err)
	}
	if v.VEN1, err = client.NewVEN(cfg("ven-1")); err != nil {
		t.Fatal(err)
	}
	if v.VEN2, err = client.NewVEN(cfg("ven-2")); err != nil {
		t.Fatal(err)
	}
	return v
}
