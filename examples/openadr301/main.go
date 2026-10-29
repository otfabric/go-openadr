// SPDX-License-Identifier: MIT

// Openadr301 is a VEN of OpenADR 3.0.1: the same client as for 3.1.0, with
// Config.Version set. It finds the programs of its group, reads an event and
// reports on it, and shows one program as JSON of either revision.
//
// This module serves 3.1.0 only, so the VTN here is a stand-in: a handler
// that answers as a 3.0.1 VTN such as OpenLEADR v0.1.4 does, for the
// requests this VEN makes. Point BaseURL at a real one instead.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/objectjson"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	vtn := httptest.NewServer(stand301())
	defer vtn.Close()

	// No TokenURL: 3.0.1 has no GET /auth/server, and the client asks
	// /auth/token of the VTN.
	ven, err := client.NewVEN(client.Config{
		BaseURL:     vtn.URL,
		Version:     openadr.Version301,
		Credentials: &client.Credentials{ClientID: "ven-1", ClientSecret: "ven-1-secret"},
	})
	if err != nil {
		return err
	}

	// Targets have a type in 3.0.1: a search names it.
	programs, err := ven.Programs(ctx, client.ProgramQuery{TargetType: "GROUP", Targets: []string{"group-a"}})
	if err != nil {
		return err
	}
	for _, p := range programs {
		fmt.Printf("program %s (%s), targets %v, attributes %v\n", p.ID, p.Name, p.TypedTargets, p.Attributes)
	}

	events, err := ven.Events(ctx, client.EventQuery{ProgramID: programs[0].ID})
	if err != nil {
		return err
	}
	e := events[0]
	fmt.Printf("event %s: %d intervals from %s\n", e.ID, len(e.Intervals), e.IntervalPeriod.Start.Format(time.RFC3339))

	// A 3.0.1 report names its program. Left empty, the client reads it
	// from the event.
	report, err := ven.CreateReport(ctx, openadr.Report{
		EventID: e.ID, ClientName: "ven-1",
		Resources: []openadr.ReportResource{{ResourceName: "meter", Intervals: []openadr.Interval{
			{ID: 0, Payloads: []openadr.ValuesMap{{Type: "USAGE", Values: []openadr.Value{openadr.Number(2.5)}}}},
		}}},
	})
	if err != nil {
		return err
	}
	fmt.Printf("report %s on event %s of program %s\n", report.ID, report.EventID, report.ProgramID)

	// What 3.0.1 does not have is refused before anything is sent.
	if _, err := ven.Notifiers(ctx); err != nil {
		fmt.Println("notifiers:", err)
	}

	// One program, as JSON of each revision. Its typed targets have no
	// 3.1.0 form: they are taken off first.
	p := programs[0]
	data301, err := objectjson.Marshal(openadr.Version301, p)
	if err != nil {
		return err
	}
	p.TypedTargets = nil
	data310, err := objectjson.Marshal(openadr.Version310, p)
	if err != nil {
		return err
	}
	fmt.Printf("3.0.1: %s\n3.1.0: %s\n", data301, data310)
	return nil
}

// stand301 answers as a 3.0.1 VTN, for the requests above.
func stand301() http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	authorized := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer token-of-ven-1" {
				reply(w, http.StatusUnauthorized, `{"title":"Unauthorized","status":401}`)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("POST /auth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("client_id") != "ven-1" || r.FormValue("client_secret") != "ven-1-secret" {
			reply(w, http.StatusUnauthorized, `{"error":"invalid_client"}`)
			return
		}
		reply(w, http.StatusOK, `{"access_token":"token-of-ven-1","token_type":"Bearer","expires_in":3600}`)
	})
	mux.HandleFunc("GET /programs", authorized(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("targetType") != "GROUP" || r.URL.Query().Get("targetValues") != "group-a" {
			reply(w, http.StatusOK, `[]`)
			return
		}
		reply(w, http.StatusOK, `[{"id":"p1","createdDateTime":"2030-01-01T00:00:00Z","modificationDateTime":"2030-01-01T00:00:00Z",
			"objectType":"PROGRAM","programName":"group-a-flex","programLongName":"Flexibility of group A","country":"NL",
			"targets":[{"type":"GROUP","values":["group-a"]}]}]`)
	}))
	mux.HandleFunc("GET /events", authorized(func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, `[`+event+`]`)
	}))
	mux.HandleFunc("GET /events/e1", authorized(func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, event)
	}))
	mux.HandleFunc("POST /reports", authorized(func(w http.ResponseWriter, r *http.Request) {
		var report map[string]any
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil || report["programID"] != "p1" {
			reply(w, http.StatusBadRequest, `{"title":"Bad Request","status":400,"detail":"programID is required"}`)
			return
		}
		report["id"], report["createdDateTime"], report["modificationDateTime"] = "r1", "2030-01-01T00:00:00Z", "2030-01-01T00:00:00Z"
		data, _ := json.Marshal(report)
		reply(w, http.StatusCreated, string(data))
	}))
	return mux
}

const event = `{"id":"e1","createdDateTime":"2030-01-01T00:00:00Z","modificationDateTime":"2030-01-01T00:00:00Z",
	"objectType":"EVENT","programID":"p1","eventName":"limit",
	"intervalPeriod":{"start":"2030-01-01T17:00:00Z","duration":"PT1H"},
	"intervals":[{"id":0,"payloads":[{"type":"IMPORT_CAPACITY_LIMIT","values":[5]}]},
	             {"id":1,"payloads":[{"type":"IMPORT_CAPACITY_LIMIT","values":[3]}]}]}`
