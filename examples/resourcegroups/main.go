// SPDX-License-Identifier: MIT

// Resourcegroups shows resource groups: Business Logic arranges the
// resources of its VENs by street and district, and each VEN sees the
// groups its own resources are in.
//
// Resource groups are not part of OpenADR 3.1.0. They are proposed for a
// later revision and implemented here as OpenLEADR implements them, so
// expect them to change; a VTN serves them only when told to.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: []auth.Client{
		{ID: "utility", Secret: "utility-secret", Scopes: auth.BusinessLogicScopes()},
		{ID: "site-1", Secret: "site-1-secret", Scopes: auth.VENScopes()},
		{ID: "site-2", Secret: "site-2-secret", Scopes: auth.VENScopes()},
	}})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	baseURL := "http://" + listener.Addr().String()
	vtn, err := server.New(server.Config{
		Store: storage.NewMemory(), Validator: issuer, TokenURL: baseURL + "/auth/token", TokenHandler: issuer,
		ResourceGroups: true,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: vtn, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	credentials := func(id string) client.Config {
		return client.Config{BaseURL: baseURL, Credentials: &client.Credentials{ClientID: id, ClientSecret: id + "-secret"}}
	}
	utility, err := client.NewBL(credentials("utility"))
	if err != nil {
		return err
	}

	// Each site registers itself and its battery.
	sites := map[string]*client.VEN{}
	batteries := map[string]openadr.Resource{}
	for _, id := range []string{"site-1", "site-2"} {
		ven, err := client.NewVEN(credentials(id))
		if err != nil {
			return err
		}
		registered, err := ven.CreateVEN(ctx, openadr.VEN{Name: id})
		if err != nil {
			return err
		}
		if batteries[id], err = ven.CreateResource(ctx, openadr.Resource{VENID: registered.ID, Name: "battery"}); err != nil {
			return err
		}
		sites[id] = ven
	}

	// The utility groups them: a street per site, and a district of both.
	resource := func(r openadr.Resource) openadr.ResourceGroupChild {
		return openadr.ResourceGroupChild{Type: openadr.ChildResource, ID: r.ID}
	}
	group := func(g openadr.ResourceGroup) openadr.ResourceGroupChild {
		return openadr.ResourceGroupChild{Type: openadr.ChildResourceGroup, ID: g.ID}
	}
	elm, err := utility.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "elm-street", Children: []openadr.ResourceGroupChild{resource(batteries["site-1"])}})
	if err != nil {
		return err
	}
	oak, err := utility.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "oak-street", Children: []openadr.ResourceGroupChild{resource(batteries["site-2"])}})
	if err != nil {
		return err
	}
	if _, err := utility.CreateResourceGroup(ctx, openadr.ResourceGroup{
		Name: "north-district", Targets: []string{"feeder-7"},
		Children: []openadr.ResourceGroupChild{group(elm), group(oak)},
	}); err != nil {
		return err
	}

	all, err := utility.ResourceGroups(ctx, client.ResourceGroupQuery{})
	if err != nil {
		return err
	}
	for _, g := range all {
		fmt.Printf("the utility sees %q with %d children\n", g.Name, len(g.Children))
	}
	// A site sees the groups that lead to its battery, and of their
	// children only what leads there.
	mine, err := sites["site-1"].ResourceGroups(ctx, client.ResourceGroupQuery{})
	if err != nil {
		return err
	}
	for _, g := range mine {
		fmt.Printf("site-1 sees %q with %d of its children\n", g.Name, len(g.Children))
	}
	return nil
}
