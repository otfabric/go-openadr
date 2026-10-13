// SPDX-License-Identifier: MIT

// Tariff is Business Logic for the command line: it publishes a schedule
// from a CSV file, such as a spreadsheet exports, as an event of a
// program, creating the program when there is none of that name.
//
//	go run ./examples/tariff -url http://127.0.0.1:8080 -client-id utility \
//	    -program day-ahead -csv prices.csv
//
// The client secret is read from $BL_CLIENT_SECRET. The file has a header
// line and the columns start, type and values, and optionally end,
// interval and randomize_start:
//
//	start,end,type,values
//	2026-10-14T00:00:00Z,2026-10-14T07:00:00Z,PRICE,0.11
//	2026-10-14T07:00:00Z,2026-10-14T23:00:00Z,PRICE,0.29
//
// Without -csv it reads standard input. With -target the event is
// addressed to the VENs that were granted that target.
//
// With -reports it publishes nothing, and prints what the VENs reported on
// the events of the program.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/timeline"
)

func main() {
	url := flag.String("url", "", "URL of the VTN's API")
	clientID := flag.String("client-id", "", "OAuth2 client ID; the secret is read from $BL_CLIENT_SECRET")
	program := flag.String("program", "", "name of the program the schedule belongs to")
	file := flag.String("csv", "", "CSV file with the schedule (default: standard input)")
	name := flag.String("name", "", "name of the event")
	target := flag.String("target", "", "target to address the event to")
	reports := flag.Bool("reports", false, "print the reports on the program's events instead of publishing")
	flag.Parse()
	if *url == "" || *clientID == "" || *program == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var err error
	if *reports {
		err = printReports(ctx, *url, *clientID, *program)
	} else {
		err = run(ctx, *url, *clientID, *program, *file, *name, *target)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func newClient(url, clientID string) (*client.BL, error) {
	return client.NewBL(client.Config{
		BaseURL: url, UserAgent: "go-openadr-example-tariff",
		Credentials: &client.Credentials{ClientID: clientID, ClientSecret: os.Getenv("BL_CLIENT_SECRET")},
	})
}

// findProgram looks for a program by name. A search cannot ask for a name,
// so this looks through all of them. The program has no ID when there is
// none of that name.
func findProgram(ctx context.Context, bl *client.BL, name string) (openadr.Program, error) {
	for p, err := range client.Each(ctx, func(ctx context.Context, page client.Page) ([]openadr.Program, error) {
		return bl.Programs(ctx, client.ProgramQuery{Page: page})
	}) {
		if err != nil {
			return openadr.Program{}, err
		}
		if p.Name == name {
			return p, nil
		}
	}
	return openadr.Program{}, nil
}

// printReports prints what VENs reported on the events of a program.
func printReports(ctx context.Context, url, clientID, programName string) error {
	bl, err := newClient(url, clientID)
	if err != nil {
		return err
	}
	program, err := findProgram(ctx, bl, programName)
	if err != nil {
		return err
	}
	if program.ID == "" {
		return fmt.Errorf("there is no program %q", programName)
	}
	count := 0
	for r, err := range client.Each(ctx, func(ctx context.Context, page client.Page) ([]openadr.Report, error) {
		return bl.Reports(ctx, client.ReportQuery{ProgramID: program.ID, Page: page})
	}) {
		if err != nil {
			return err
		}
		count++
		for _, res := range r.Resources {
			// A report is laid out like an event: here only to count.
			rows, err := timeline.Of(res.IntervalPeriod, res.Intervals, timeline.Options{})
			total := 0.0
			for _, iv := range res.Intervals {
				for _, payload := range iv.Payloads {
					for _, v := range payload.Values {
						if n, ok := v.Number(); ok && payload.Type == "USAGE" {
							total += n
						}
					}
				}
			}
			fmt.Printf("report from %q on event %s: %s used %.2f kWh over %d intervals", r.ClientID, r.EventID, res.ResourceName, total, len(res.Intervals))
			if err == nil && len(rows) > 0 {
				start, _ := rows.Span()
				fmt.Printf(", from %s", start.Format(time.RFC3339))
			}
			fmt.Println()
		}
	}
	fmt.Printf("%d reports on the program %q\n", count, programName)
	return nil
}

func run(ctx context.Context, url, clientID, programName, file, name, target string) error {
	var in io.Reader = os.Stdin
	if file != "" {
		f, err := os.Open(file) //nolint:gosec // the file the user named
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		in = f
	}
	table, err := timeline.ReadCSV(in)
	if err != nil {
		return err
	}
	if len(table) == 0 {
		return errors.New("the schedule is empty")
	}

	bl, err := newClient(url, clientID)
	if err != nil {
		return err
	}

	// Program names are unique: find it, or make it.
	program, err := findProgram(ctx, bl, programName)
	if err != nil {
		return err
	}
	if program.ID == "" {
		// A descriptor for every type of payload in the schedule, so that
		// a VEN can tell what the numbers are.
		p := openadr.Program{Name: programName}
		seen := map[string]bool{}
		for _, row := range table {
			if !seen[row.Type] {
				seen[row.Type] = true
				p.PayloadDescriptors = append(p.PayloadDescriptors, openadr.EventPayloadDescriptor{PayloadType: row.Type})
			}
		}
		if program, err = bl.CreateProgram(ctx, p); err != nil {
			// Another client made it meanwhile: openadr.ErrConflict.
			return fmt.Errorf("creating the program: %w", err)
		}
		fmt.Printf("created the program %q (%s)\n", program.Name, program.ID)
	}

	event := openadr.Event{ProgramID: program.ID, Name: name, Intervals: table.Intervals()}
	if target != "" {
		event.Targets = []string{target}
	}
	created, err := bl.CreateEvent(ctx, event)
	var invalid *openadr.ValidationError
	if errors.As(err, &invalid) {
		// Refused before it was sent: the schedule does not make an event.
		return fmt.Errorf("the schedule is not a valid event: %s %s", invalid.Field, invalid.Reason)
	}
	if err != nil {
		return err
	}
	start, end := table.Span()
	fmt.Printf("published the event %s: %d intervals from %s", created.ID, len(created.Intervals), start.Format(time.RFC3339))
	if !end.IsZero() {
		fmt.Printf(" to %s", end.Format(time.RFC3339))
	}
	fmt.Println()
	return nil
}
