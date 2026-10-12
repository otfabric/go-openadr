// SPDX-License-Identifier: MIT

// Notifications shows a VEN that is told of new events instead of polling
// for them: it runs a webhook, subscribes to events at the VTN, and the VTN
// calls the webhook when Business Logic publishes one.
//
// Everything runs in one process over loopback HTTP, which is why the VTN's
// webhook sender is told to allow it. A deployment uses https throughout.
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
	"github.com/otfabric/go-openadr/notification/webhook"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// serve runs handler on a loopback port and returns its URL.
func serve(handler http.Handler) (url string, stop func(), err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	return "http://" + listener.Addr().String(), func() { _ = srv.Close() }, nil
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The VTN, with a sender for its notifications.
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: []auth.Client{
		{ID: "utility", Secret: "utility-secret", Scopes: auth.BusinessLogicScopes()},
		{ID: "site-42", Secret: "site-42-secret", Scopes: auth.VENScopes()},
	}})
	if err != nil {
		return err
	}
	sender := webhook.NewSender(webhook.SenderConfig{AllowInsecure: true})
	defer func() { _ = sender.Close(ctx) }()
	mux := http.NewServeMux()
	vtnURL, stopVTN, err := serve(mux)
	if err != nil {
		return err
	}
	defer stopVTN()
	vtn, err := server.New(server.Config{
		Store:        storage.NewMemory(),
		Validator:    issuer,
		TokenURL:     vtnURL + "/auth/token",
		TokenHandler: issuer,
		Webhooks:     sender,
	})
	if err != nil {
		return err
	}
	mux.Handle("/", vtn)

	// The VEN's webhook. The token is one the VEN makes up and gives the
	// VTN in the subscription, so that nobody else can feed it events.
	const hookToken = "a-token-only-the-vtn-is-given"
	events := make(chan openadr.Event, 1)
	hookURL, stopHook, err := serve(&webhook.Handler{
		BearerToken: hookToken,
		Notify: func(_ context.Context, n openadr.Notification) error {
			if event, ok := n.Object.(openadr.Event); ok && n.Operation == openadr.OperationCreate {
				events <- event
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	defer stopHook()

	// The VEN subscribes. The VTN calls the webhook once to verify it
	// before it accepts the subscription.
	ven, err := client.NewVEN(client.Config{
		BaseURL:     vtnURL,
		Credentials: &client.Credentials{ClientID: "site-42", ClientSecret: "site-42-secret"},
	})
	if err != nil {
		return err
	}
	subscription, err := ven.CreateSubscription(ctx, openadr.Subscription{
		ClientName: "site-42",
		ObjectOperations: []openadr.ObjectOperation{{
			Objects:     []openadr.ObjectType{openadr.ObjectEvent},
			Operations:  []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate, openadr.OperationDelete},
			CallbackURL: hookURL + "/openadr",
			BearerToken: hookToken,
		}},
	})
	if err != nil {
		return err
	}
	fmt.Printf("VEN subscribed to events (subscription of client %q)\n", subscription.ClientID)

	// Business Logic publishes an event.
	bl, err := client.NewBL(client.Config{
		BaseURL:     vtnURL,
		Credentials: &client.Credentials{ClientID: "utility", ClientSecret: "utility-secret"},
	})
	if err != nil {
		return err
	}
	program, err := bl.CreateProgram(ctx, openadr.Program{Name: "dynamic-tariff"})
	if err != nil {
		return err
	}
	if _, err := bl.CreateEvent(ctx, openadr.Event{
		ProgramID: program.ID,
		Name:      "evening-prices",
		Intervals: []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(0.31)}}}}},
	}); err != nil {
		return err
	}

	// The VEN hears of it without asking.
	select {
	case event := <-events:
		price, _ := event.Intervals[0].Payloads[0].Values[0].Number()
		fmt.Printf("VEN was notified of event %q: %.2f EUR/kWh\n", event.Name, price)
	case <-ctx.Done():
		return fmt.Errorf("no notification arrived: %w", ctx.Err())
	}
	return nil
}
