// SPDX-License-Identifier: MIT

// Prices runs a VTN that publishes a public tariff to anyone who asks, as
// the specification allows for public information ("Non-Authenticating
// VTNs"), and a client that reads it without credentials and lays the
// prices out as a table.
//
// The utility writes the tariff with its own credentials; reading needs
// none. The event is written the compact way: one start, one duration,
// several prices packed into an interval, and a duration of the event that
// repeats the day. Package timeline works out what that means.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
	"github.com/otfabric/go-openadr/timeline"
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
		// Requests without a token are answered: programs and events that
		// have no targets can be read by anyone. Everything else, and
		// every write, still needs a token.
		AllowAnonymous: true,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: vtn, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	// The utility publishes two days of prices: a night, a day and an
	// evening price, each for eight hours, repeated.
	utility, err := client.NewBL(client.Config{
		BaseURL:     baseURL,
		Credentials: &client.Credentials{ClientID: "utility", ClientSecret: "utility-secret"},
	})
	if err != nil {
		return err
	}
	program, err := utility.CreateProgram(ctx, openadr.Program{
		Name:               "public-tariff",
		PayloadDescriptors: []openadr.PayloadDescriptor{openadr.EventPayloadDescriptor{PayloadType: "PRICE", Units: "KWH", Currency: "EUR"}},
	})
	if err != nil {
		return err
	}
	midnight := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	if _, err := utility.CreateEvent(ctx, openadr.Event{
		ProgramID:      program.ID,
		Name:           "prices",
		Duration:       &openadr.Duration{Days: 2},
		IntervalPeriod: &openadr.IntervalPeriod{Start: &midnight, Duration: &openadr.Duration{Hours: 24}},
		Intervals: []openadr.Interval{{ID: 0, Payloads: []openadr.ValuesMap{{
			Type: "PRICE", Values: []openadr.Value{openadr.Number(0.11), openadr.Number(0.27), openadr.Number(0.34)},
		}}}},
	}); err != nil {
		return err
	}

	// Anyone reads them: a client with the URL and nothing else sends no
	// token.
	anyone, err := client.NewVEN(client.Config{BaseURL: baseURL})
	if err != nil {
		return err
	}
	events, err := anyone.Events(ctx, client.EventQuery{})
	if err != nil {
		return err
	}
	for _, event := range events {
		rows, err := timeline.OfEvent(event, timeline.Options{SubIntervals: true})
		if err != nil {
			return err
		}
		fmt.Printf("%s, as a table:\n", event.Name)
		if err := rows.WriteCSV(os.Stdout); err != nil {
			return err
		}
		at := midnight.Add(34 * time.Hour)
		for _, row := range rows.At(at) {
			price, _ := row.Values[0].Number()
			fmt.Printf("the price at %s is %.2f EUR/kWh, until %s\n", at.Format("Mon 15:04"), price, row.End.Format("Mon 15:04"))
		}
	}

	// What is not public is not for a client without a token.
	if _, err := anyone.Reports(ctx, client.ReportQuery{}); err != nil {
		fmt.Println("reading reports without a token:", err)
	}
	return nil
}
