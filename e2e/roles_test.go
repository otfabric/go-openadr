// SPDX-License-Identifier: MIT

package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The three roles of an OpenADR system as three programs, each a process
// of its own, as they are deployed: a VTN, a Business Logic that publishes
// a tariff, and a VEN. They are the examples of this module, built and run
// as a user would run them:
//
//	publish a 24-hour tariff → the VTN stores the event → the VEN retrieves
//	the prices → the VEN submits a report → the publisher retrieves it.
//
// Everything else in this package drives the library; this test drives
// the programs, and so also keeps the examples working together.
func TestThreeRolesAsPrograms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stops the VTN with an interrupt signal")
	}
	if testing.Short() {
		t.Skip("builds and runs three programs")
	}
	dir := t.TempDir()
	build := exec.Command("go", "build", "-o", dir+string(filepath.Separator), "./examples/vtn", "./examples/tariff", "./examples/ven")
	build.Dir = ".."
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the examples: %v\n%s", err, out)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	url := "http://" + addr

	// run runs one of the programs to its end and returns what it printed.
	run := func(env, name string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(dir, name), args...)
		cmd.Env = append(os.Environ(), env)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		t.Logf("$ %s %s\n%s", name, strings.Join(args, " "), out)
		return string(out)
	}
	// startVTN starts the VTN and returns a function that stops it.
	store := filepath.Join(dir, "vtn.journal")
	startVTN := func() (stop func()) {
		t.Helper()
		var log bytes.Buffer
		cmd := exec.Command(filepath.Join(dir, "vtn"), "-addr", addr, "-store", store,
			"-client", "utility:utility-secret:bl", "-client", "site-42:site-42-secret:ven")
		cmd.Stdout, cmd.Stderr = &log, &log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		stopped := false
		stop = func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("the VTN did not stop cleanly: %v\n%s", err, log.String())
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				t.Errorf("the VTN did not stop when interrupted\n%s", log.String())
			}
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := http.Get(url + "/auth/server")
			if err == nil {
				_ = resp.Body.Close()
				return stop
			}
			if time.Now().After(deadline) {
				t.Fatalf("the VTN does not answer: %v\n%s", err, log.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Tomorrow's prices, one for every hour, as a supplier's spreadsheet
	// exports them.
	tomorrow := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	var sheet strings.Builder
	sheet.WriteString("start,end,type,values\n")
	for hour := range 24 {
		from := tomorrow.Add(time.Duration(hour) * time.Hour)
		fmt.Fprintf(&sheet, "%s,%s,PRICE,0.%02d\n", from.Format(time.RFC3339), from.Add(time.Hour).Format(time.RFC3339), 10+hour)
	}
	prices := filepath.Join(dir, "prices.csv")
	if err := os.WriteFile(prices, []byte(sheet.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. The publisher, Business Logic, publishes the tariff on the VTN.
	stop := startVTN()
	out := run("BL_CLIENT_SECRET=utility-secret", "tariff", "-url", url, "-client-id", "utility", "-program", "day-ahead", "-csv", prices, "-name", "tomorrow")
	if !strings.Contains(out, `created the program "day-ahead"`) || !strings.Contains(out, "24 intervals from "+tomorrow.Format(time.RFC3339)) {
		t.Fatalf("the publisher said:\n%s", out)
	}

	// 2. The VTN stores it: it is stopped and started, and still has it.
	stop()
	startVTN()

	// 3. The VEN retrieves the prices, 4. and reports its usage.
	out = run("VEN_CLIENT_SECRET=site-42-secret", "ven", "-url", url, "-client-id", "site-42", "-report")
	if !strings.Contains(out, `event "tomorrow"`) || strings.Count(out, "PRICE") != 24 || !strings.Contains(out, "[0.11]") || !strings.Contains(out, "[0.33]") {
		t.Errorf("the VEN did not print the 24 prices:\n%s", out)
	}
	if !strings.Contains(out, `registered as "site-42"`) || !strings.Contains(out, "reported the usage of 24 intervals") || !strings.Contains(out, "1 active events") {
		t.Errorf("the VEN said:\n%s", out)
	}

	// 5. The publisher retrieves the report.
	out = run("BL_CLIENT_SECRET=utility-secret", "tariff", "-url", url, "-client-id", "utility", "-program", "day-ahead", "-reports")
	// Six times 1, 1.25, 1.5 and 1.75 kWh.
	if !strings.Contains(out, `report from "site-42"`) || !strings.Contains(out, "main-meter used 33.00 kWh over 24 intervals") || !strings.Contains(out, `1 reports on the program "day-ahead"`) {
		t.Errorf("the publisher read:\n%s", out)
	}

	// The roles are kept apart: the VEN's credentials do not publish.
	cmd := exec.Command(filepath.Join(dir, "tariff"), "-url", url, "-client-id", "site-42", "-program", "mine", "-csv", prices)
	cmd.Env = append(os.Environ(), "BL_CLIENT_SECRET=site-42-secret")
	if refused, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(refused), "403") {
		t.Errorf("a VEN publishing a tariff: %v\n%s", err, refused)
	}
}
