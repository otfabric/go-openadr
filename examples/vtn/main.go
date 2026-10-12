// SPDX-License-Identifier: MIT

// Vtn runs a standalone VTN with an in-memory store and its own token
// endpoint, for trying out clients and for interoperability tests. Objects
// and tokens are lost when it stops.
//
//	go run ./examples/vtn -addr 127.0.0.1:8080 \
//	    -client utility:utility-secret:bl -client site-42:site-42-secret:ven
//
// Clients are told where to get a token by URL. When they reach the VTN under
// another address than -addr, give that address with -public-url.
//
// It serves plain HTTP: put it behind a TLS-terminating proxy for anything
// but local use.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// clientFlags collects the -client flags.
type clientFlags []auth.Client

func (c *clientFlags) String() string { return fmt.Sprintf("%d clients", len(*c)) }

func (c *clientFlags) Set(v string) error {
	parts := strings.Split(v, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return errors.New("want id:secret:role")
	}
	client := auth.Client{ID: parts[0], Secret: parts[1]}
	switch parts[2] {
	case "bl":
		client.Scopes = auth.BusinessLogicScopes()
	case "ven":
		client.Scopes = auth.VENScopes()
	default:
		return errors.New("role must be bl or ven")
	}
	*c = append(*c, client)
	return nil
}

func main() {
	var clients clientFlags
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	publicURL := flag.String("public-url", "", "URL under which clients reach the API (default http://<addr>)")
	flag.Var(&clients, "client", "an OAuth2 client as id:secret:role, role being bl or ven; repeatable")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(clients) == 0 {
		logger.Error("no -client given: nobody could authenticate")
		os.Exit(2)
	}
	if *publicURL == "" {
		*publicURL = "http://" + *addr
	}
	if err := run(*addr, strings.TrimSuffix(*publicURL, "/"), clients, logger); err != nil {
		logger.Error("vtn stopped", "error", err)
		os.Exit(1)
	}
}

func run(addr, publicURL string, clients []auth.Client, logger *slog.Logger) error {
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: clients})
	if err != nil {
		return err
	}
	vtn, err := server.New(server.Config{
		Store:        storage.NewMemory(),
		Validator:    issuer,
		TokenURL:     publicURL + "/auth/token",
		TokenHandler: issuer,
		Logger:       logger,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           vtn,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	logger.Info("vtn listening", "addr", addr, "clients", len(clients))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
