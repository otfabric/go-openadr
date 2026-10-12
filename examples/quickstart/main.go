// SPDX-License-Identifier: MIT

// Quickstart runs the basic OpenADR 3 workflow in one process: Business
// Logic creates a program and an event on a VTN, a VEN discovers them and
// sends a report, and Business Logic reads the report.
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

	// The VTN: an in-memory store, and a token service for two clients.
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
		Store:     storage.NewMemory(),
		Validator: issuer,
		// The VTN serves its own token endpoint and tells clients where it is.
		TokenURL:     baseURL + "/auth/token",
		TokenHandler: issuer,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: vtn, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	// Business Logic defines a program and issues an event with two prices.
	bl, err := client.NewBL(client.Config{
		BaseURL:     baseURL,
		Credentials: &client.Credentials{ClientID: "utility", ClientSecret: "utility-secret"},
	})
	if err != nil {
		return err
	}
	program, err := bl.CreateProgram(ctx, openadr.Program{
		Name: "dynamic-tariff",
		PayloadDescriptors: []openadr.PayloadDescriptor{
			openadr.EventPayloadDescriptor{PayloadType: "PRICE", Units: "KWH", Currency: "EUR"},
		},
	})
	if err != nil {
		return err
	}
	start := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	price := func(id int32, eur float64) openadr.Interval {
		return openadr.Interval{ID: id, Payloads: []openadr.ValuesMap{{Type: "PRICE", Values: []openadr.Value{openadr.Number(eur)}}}}
	}
	if _, err := bl.CreateEvent(ctx, openadr.Event{
		ProgramID:         program.ID,
		Name:              "evening-prices",
		IntervalPeriod:    &openadr.IntervalPeriod{Start: &start, Duration: &openadr.Duration{Hours: 1}},
		Intervals:         []openadr.Interval{price(0, 0.31), price(1, 0.24)},
		ReportDescriptors: []openadr.ReportDescriptor{openadr.NewReportDescriptor("USAGE")},
	}); err != nil {
		return err
	}

	// The VEN finds the program and its events, and reports its usage.
	ven, err := client.NewVEN(client.Config{
		BaseURL:     baseURL,
		Credentials: &client.Credentials{ClientID: "site-42", ClientSecret: "site-42-secret"},
	})
	if err != nil {
		return err
	}
	programs, err := ven.Programs(ctx, client.ProgramQuery{})
	if err != nil {
		return err
	}
	for _, p := range programs {
		fmt.Printf("VEN sees program %q\n", p.Name)
		events, err := ven.Events(ctx, client.EventQuery{ProgramID: p.ID})
		if err != nil {
			return err
		}
		for _, e := range events {
			fmt.Printf("VEN sees event %q, starting %s\n", e.Name, e.IntervalPeriod.Start.Format(time.RFC3339))
			for _, iv := range e.Intervals {
				eur, _ := iv.Payloads[0].Values[0].Number()
				fmt.Printf("  interval %d: %.2f EUR/kWh\n", iv.ID, eur)
			}
			if _, err := ven.CreateReport(ctx, openadr.Report{
				EventID:    e.ID,
				ClientName: "site-42",
				Resources: []openadr.ReportResource{{
					ResourceName: "main-meter",
					Intervals: []openadr.Interval{
						{ID: 0, Payloads: []openadr.ValuesMap{{Type: "USAGE", Values: []openadr.Value{openadr.Number(1.8)}}}},
					},
				}},
			}); err != nil {
				return err
			}
		}
	}

	// Business Logic reads what was reported for the program.
	reports, err := bl.Reports(ctx, client.ReportQuery{ProgramID: program.ID})
	if err != nil {
		return err
	}
	for _, r := range reports {
		kwh, _ := r.Resources[0].Intervals[0].Payloads[0].Values[0].Number()
		fmt.Printf("Business Logic reads a report from client %q: %s used %.1f kWh\n", r.ClientID, r.Resources[0].ResourceName, kwh)
	}
	return nil
}
