// SPDX-License-Identifier: MIT

// Websocket shows notifications over a WebSocket: a VEN subscribes to the
// events of a program and is told of them on a connection it opened
// itself, so it needs no address the VTN can reach (as a webhook does) and
// no broker (as MQTT does).
//
// WebSocket notifications are not part of OpenADR 3.1.0. They are an
// extension of OpenLEADR that this module shares; GET /notifiers says
// whether a VTN has them.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/websocket"
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
		{ID: "site-42", Secret: "site-42-secret", Scopes: auth.VENScopes()},
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
		Store:        storage.NewMemory(),
		Validator:    issuer,
		TokenURL:     baseURL + "/auth/token",
		TokenHandler: issuer,
		// The zero configuration is a usable one.
		WebSocket: &server.WebSocketConfig{},
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: vtn, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	// The WebSockets are the VTN's to close, not the HTTP server's.
	defer func() {
		_ = vtn.Close()
		_ = srv.Close()
	}()

	utility, err := client.NewBL(client.Config{BaseURL: baseURL, Credentials: &client.Credentials{ClientID: "utility", ClientSecret: "utility-secret"}})
	if err != nil {
		return err
	}
	ven, err := client.NewVEN(client.Config{BaseURL: baseURL, Credentials: &client.Credentials{ClientID: "site-42", ClientSecret: "site-42-secret"}})
	if err != nil {
		return err
	}
	program, err := utility.CreateProgram(ctx, openadr.Program{Name: "flex"})
	if err != nil {
		return err
	}

	// The VEN asks how the VTN notifies, and subscribes accordingly.
	notifiers, err := ven.Notifiers(ctx)
	if err != nil {
		return err
	}
	if !notifiers.WebSocket {
		return errors.New("this VTN does not notify over WebSockets")
	}
	if _, err := ven.CreateSubscription(ctx, openadr.Subscription{
		ClientName: "site-42",
		ProgramID:  program.ID,
		ObjectOperations: []openadr.ObjectOperation{{
			Objects:    []openadr.ObjectType{openadr.ObjectEvent},
			Operations: []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate, openadr.OperationDelete},
			Mechanism:  openadr.MechanismWebSocket,
		}},
	}); err != nil {
		return err
	}

	// The Subscriber keeps the WebSocket open, and opens it again when it
	// is lost. What happened in between it does not hear: a program that
	// must not miss an event searches after every reconnect, which
	// Sessions tells of.
	told := make(chan openadr.Notification, 8)
	subscriber, err := websocket.NewSubscriber(websocket.SubscriberConfig{
		Dial:   ven.NotificationStream,
		Notify: func(_ context.Context, n openadr.Notification) { told <- n },
	})
	if err != nil {
		return err
	}
	defer func() { _ = subscriber.Close() }()
	if err := subscriber.Ready(ctx); err != nil {
		return err
	}

	// The utility issues an event, changes it, and takes it back.
	event, err := utility.CreateEvent(ctx, openadr.Event{ProgramID: program.ID, Name: "reduce-load"})
	if err != nil {
		return err
	}
	event.Priority = new(int)
	if _, err := utility.UpdateEvent(ctx, event); err != nil {
		return err
	}
	if _, err := utility.DeleteEvent(ctx, event.ID); err != nil {
		return err
	}
	for range 3 {
		select {
		case n := <-told:
			fmt.Printf("the VEN was told: %s %s %q\n", n.Operation, n.ObjectType, n.Object.(openadr.Event).Name)
		case <-ctx.Done():
			return errors.New("a notification did not arrive")
		}
	}
	fmt.Printf("over %d connection\n", subscriber.Sessions())
	return nil
}
