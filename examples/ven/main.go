// SPDX-License-Identifier: MIT

// Ven is a small VEN for the command line: it finds or is told a VTN,
// registers itself, and prints what the events it may see ask of it, as a
// table. With -report it answers each event with a report of its usage,
// one value per interval, and with -watch it stays and prints every change
// it is notified of.
//
//	go run ./examples/ven -url http://127.0.0.1:8080 -client-id site-42
//	go run ./examples/ven -discover -client-id site-42 -watch
//
// The client secret is read from $VEN_CLIENT_SECRET. Without -client-id no
// token is asked for, which a VTN with public information accepts.
//
// Try it against the VTN of this module:
//
//	go run ./examples/vtn -client utility:utility-secret:bl -client site-42:site-42-secret:ven -websocket
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/discovery"
	"github.com/otfabric/go-openadr/notification/websocket"
	"github.com/otfabric/go-openadr/timeline"
)

func main() {
	url := flag.String("url", "", "URL of the VTN's API")
	discover := flag.Bool("discover", false, "find the VTN on the local network instead of naming it with -url")
	clientID := flag.String("client-id", "", "OAuth2 client ID; the secret is read from $VEN_CLIENT_SECRET")
	name := flag.String("name", "", "name to register the VEN under (default: the client ID)")
	insecure := flag.Bool("insecure", false, "accept a TLS certificate that cannot be verified, as of a gateway with a self-signed one")
	watch := flag.Bool("watch", false, "stay, and print the events the VTN notifies of")
	targets := flag.String("target", "", "a target to name when searching, to see what is addressed to it")
	flag.BoolVar(&report, "report", false, "send a report of the usage in every interval of each event (made-up numbers)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *url, *discover, *clientID, *name, *targets, *insecure, *watch); err != nil {
		log.Fatal(err)
	}
}

// report says whether to answer events with reports.
var report bool

func run(ctx context.Context, url string, discover bool, clientID, name, target string, insecure, watch bool) error {
	cfg := client.Config{BaseURL: url, UserAgent: "go-openadr-example-ven"}
	if discover {
		vtns, err := discovery.Browse(ctx, discovery.Options{First: true})
		if err != nil {
			return err
		}
		if len(vtns) == 0 {
			return errors.New("no VTN answered on the local network")
		}
		fmt.Printf("found the VTN %q at %s\n", vtns[0].Instance, vtns[0].URL())
		cfg.BaseURL = vtns[0].URL()
		if !vtns[0].RequiresAuth {
			clientID = ""
		}
	}
	if cfg.BaseURL == "" {
		return errors.New("give -url or -discover")
	}
	if insecure {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // asked for with -insecure
		}}
	}
	if clientID != "" {
		cfg.Credentials = &client.Credentials{ClientID: clientID, ClientSecret: os.Getenv("VEN_CLIENT_SECRET")}
	}
	ven, err := client.NewVEN(cfg)
	if err != nil {
		return err
	}

	// A VEN with credentials has a ven object, which Business Logic grants
	// targets through. One without only reads.
	if clientID != "" {
		if name == "" {
			name = clientID
		}
		mine, err := ven.VENs(ctx, client.VENQuery{})
		if err != nil {
			return err
		}
		if len(mine) == 0 {
			registered, err := ven.CreateVEN(ctx, openadr.VEN{Name: name})
			if err != nil {
				return fmt.Errorf("registering: %w", err)
			}
			mine = append(mine, registered)
			fmt.Printf("registered as %q (%s)\n", registered.Name, registered.ID)
		}
		fmt.Printf("ven %q has been granted the targets %v\n", mine[0].Name, mine[0].Targets)
	}

	query := client.EventQuery{ActiveOnly: true}
	if target != "" {
		query.Targets = []string{target}
	}
	// Each walks the pages of a search.
	count := 0
	for event, err := range client.Each(ctx, func(ctx context.Context, p client.Page) ([]openadr.Event, error) {
		query.Page = p
		return ven.Events(ctx, query)
	}) {
		if err != nil {
			return err
		}
		count++
		printEvent(event)
		if report && clientID != "" {
			if err := sendReport(ctx, ven, event, name); err != nil {
				return fmt.Errorf("reporting on %s: %w", event.ID, err)
			}
		}
	}
	fmt.Printf("%d active events\n", count)
	if !watch {
		return nil
	}

	// Notifications over a WebSocket, where the VTN has them. Where it
	// has not, a VEN polls, or subscribes by webhook or MQTT: see
	// examples/notifications.
	notifiers, err := ven.Notifiers(ctx)
	if err != nil {
		return err
	}
	if !notifiers.WebSocket || clientID == "" {
		return errors.New("-watch needs a VTN with WebSocket notifications, and credentials")
	}
	sub, err := ven.CreateSubscription(ctx, openadr.Subscription{ClientName: name, Targets: query.Targets, ObjectOperations: []openadr.ObjectOperation{{
		Objects:    []openadr.ObjectType{openadr.ObjectEvent},
		Operations: []openadr.Operation{openadr.OperationCreate, openadr.OperationUpdate, openadr.OperationDelete},
		Mechanism:  openadr.MechanismWebSocket,
	}}})
	if err != nil {
		return err
	}
	defer func() {
		// The context is done by now; the subscription is not left behind.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = ven.DeleteSubscription(cleanup, sub.ID)
	}()
	listener, err := websocket.NewSubscriber(websocket.SubscriberConfig{
		Dial: ven.NotificationStream,
		Notify: func(_ context.Context, n openadr.Notification) {
			if event, ok := n.Object.(openadr.Event); ok {
				fmt.Printf("%s: ", n.Operation)
				printEvent(event)
			}
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	fmt.Println("watching; interrupt to stop")
	<-ctx.Done()
	return nil
}

// sendReport tells the VTN what was used in each interval of an event. A
// real VEN reads that from a meter; this one makes it up. The report uses
// the interval IDs of the event, which is how Business Logic can tell
// which usage goes with which price.
func sendReport(ctx context.Context, ven *client.VEN, event openadr.Event, name string) error {
	intervals := make([]openadr.Interval, len(event.Intervals))
	for i, iv := range event.Intervals {
		intervals[i] = openadr.Interval{ID: iv.ID, Payloads: []openadr.ValuesMap{
			{Type: "USAGE", Values: []openadr.Value{openadr.Number(1 + float64(i%4)/4)}},
		}}
	}
	sent, err := ven.CreateReport(ctx, openadr.Report{
		EventID: event.ID, ClientName: name,
		PayloadDescriptors: []openadr.ReportPayloadDescriptor{{PayloadType: "USAGE", Units: "KWH"}},
		Resources:          []openadr.ReportResource{{ResourceName: "main-meter", Intervals: intervals}},
	})
	if err != nil {
		return err
	}
	fmt.Printf("  reported the usage of %d intervals (%s)\n", len(intervals), sent.ID)
	return nil
}

func printEvent(event openadr.Event) {
	fmt.Printf("event %q (%s)\n", event.Name, event.ID)
	// A day ahead at most, of an event that repeats.
	rows, err := timeline.OfEvent(event, timeline.Options{SubIntervals: true, Until: time.Now().Add(24 * time.Hour)})
	if err != nil {
		fmt.Printf("  its intervals cannot be laid out: %v\n", err)
		return
	}
	for _, row := range rows {
		end := "open"
		if !row.End.IsZero() {
			end = row.End.Local().Format("Jan 2 15:04")
		}
		fmt.Printf("  %s - %s  %-22s %v\n", row.Start.Local().Format("Jan 2 15:04"), end, row.Type, row.Values)
	}
	if now := rows.At(time.Now()); len(now) > 0 {
		fmt.Printf("  now: %s %v\n", now[0].Type, now[0].Values)
	}
}
