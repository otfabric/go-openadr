// SPDX-License-Identifier: MIT

// Vtn runs the VTN of go-openadr on a SQL database: a SQLite file, or a
// PostgreSQL server. It is the proof that the store is the VTN's only
// state: stop it, start it again, and everything is where it was.
//
//	go run ./vtn -sqlite vtn.db -client utility:utility-secret:bl -client site-42:site-42-secret:ven
//
//	OPENADR_POSTGRES_DSN=postgres://user:password@localhost:5432/openadr \
//	    go run ./vtn -postgres -client utility:utility-secret:bl -client site-42:site-42-secret:ven
//
// With PostgreSQL several of these can run on one database at once, behind
// a load balancer: they share nothing else. (Tokens are the exception in
// this example: each process issues its own, in memory. A deployment like
// that validates the tokens of an identity provider: auth.JWTValidator.)
//
// It serves plain HTTP, for trying out. The examples of the main module
// are its clients: examples/tariff and examples/ven.
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
	"github.com/otfabric/go-openadr/examples/storage/postgres"
	"github.com/otfabric/go-openadr/examples/storage/sqlite"
	"github.com/otfabric/go-openadr/examples/storage/sqlstore"
	"github.com/otfabric/go-openadr/server"
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
	file := flag.String("sqlite", "", "keep the VTN's objects in this SQLite file")
	pg := flag.Bool("postgres", false, "keep the VTN's objects in the PostgreSQL database named by $OPENADR_POSTGRES_DSN")
	flag.Var(&clients, "client", "an OAuth2 client as id:secret:role, role being bl or ven; repeatable")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*addr, *file, *pg, clients, logger); err != nil {
		logger.Error("vtn stopped", "error", err)
		os.Exit(1)
	}
}

func run(addr, file string, pg bool, clients []auth.Client, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The one line that differs between the databases.
	var (
		store *sqlstore.Store
		err   error
	)
	switch {
	case pg && file != "":
		return errors.New("give -sqlite or -postgres, not both")
	case pg:
		// The connection string holds a password: from the environment,
		// not from a flag that every user of the machine can read.
		store, err = postgres.Open(ctx, os.Getenv("OPENADR_POSTGRES_DSN"))
	case file != "":
		store, err = sqlite.Open(ctx, file)
	default:
		return errors.New("give -sqlite FILE or -postgres")
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: clients})
	if err != nil {
		return err
	}
	vtn, err := server.New(server.Config{
		Store:          store,
		Validator:      issuer,
		TokenHandler:   issuer,
		TokenURL:       "http://" + addr + "/auth/token",
		ResourceGroups: true,
		Logger:         logger,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: vtn, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	logger.Info("vtn listening", "addr", addr, "clients", len(clients))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-stopped
	return nil
}
