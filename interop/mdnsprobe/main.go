// SPDX-License-Identifier: MIT

// Mdnsprobe puts package discovery on a network for the interoperability
// tests: it announces a VTN until it is stopped, or looks for VTNs and
// prints what it found as JSON. The tests build it for Linux and run it in
// a container beside another implementation of Multicast DNS, because
// multicast does not cross from a test process into a container network.
//
//	mdnsprobe advertise -instance "Go VTN" -host govtn -port 8443 -base-path openadr3
//	mdnsprobe browse -timeout 5s
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/otfabric/go-openadr/discovery"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mdnsprobe advertise|browse [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "advertise":
		err = advertise(os.Args[2:])
	case "browse":
		err = browse(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func advertise(args []string) error {
	fs := flag.NewFlagSet("advertise", flag.ExitOnError)
	ad := discovery.Advertisement{Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	fs.StringVar(&ad.Instance, "instance", "", "instance name")
	fs.StringVar(&ad.Host, "host", "", "host name, without .local")
	fs.IntVar(&ad.Port, "port", 8443, "port of the API")
	fs.StringVar(&ad.BasePath, "base-path", "", "base path of the API")
	fs.StringVar(&ad.LocalURL, "local-url", "", "URL of the API, when not the default")
	fs.BoolVar(&ad.NoAuth, "no-auth", false, "announce a VTN that does not authenticate")
	programs := fs.String("programs", "", "program names, separated by commas")
	_ = fs.Parse(args)
	if *programs != "" {
		ad.ProgramNames = strings.Split(*programs, ",")
	}
	a, err := discovery.Advertise(ad)
	if err != nil {
		return err
	}
	fmt.Println("advertising")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return a.Close()
}

func browse(args []string) error {
	fs := flag.NewFlagSet("browse", flag.ExitOnError)
	timeout := fs.Duration("timeout", 5*time.Second, "how long to listen")
	first := fs.Bool("first", false, "stop at the first VTN")
	_ = fs.Parse(args)
	vtns, err := discovery.Browse(context.Background(), discovery.Options{Timeout: *timeout, First: *first})
	if err != nil {
		return err
	}
	if vtns == nil {
		vtns = []discovery.VTN{}
	}
	return json.NewEncoder(os.Stdout).Encode(vtns)
}
