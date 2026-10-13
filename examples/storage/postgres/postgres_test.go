// SPDX-License-Identifier: MIT

package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/examples/storage/internal/vtntest"
	"github.com/otfabric/go-openadr/examples/storage/postgres"
	"github.com/otfabric/go-openadr/examples/storage/sqlstore"
	"github.com/otfabric/go-openadr/server/storage"
	"github.com/otfabric/go-openadr/server/storage/storagetest"
)

// These tests need a PostgreSQL server. They take the one named by
// OPENADR_POSTGRES_DSN, a connection string to a database they may create
// schemas in, or start one with Docker (the image is OPENADR_POSTGRES_IMAGE,
// postgres:18-alpine by default). Without either they are skipped, unless
// OPENADR_STORAGE_REQUIRE is set, as in CI: then they fail.

var (
	server     sync.Once
	serverDSN  string
	serverFail string
	schemas    atomic.Int64
)

// dsn returns a connection string to a server, starting one if need be.
func dsn(t *testing.T) string {
	t.Helper()
	server.Do(func() {
		if serverDSN = os.Getenv("OPENADR_POSTGRES_DSN"); serverDSN != "" {
			return
		}
		image := os.Getenv("OPENADR_POSTGRES_IMAGE")
		if image == "" {
			image = "postgres:18-alpine"
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			serverFail = err.Error()
			return
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		out, err := exec.Command("docker", "run", "-d", "--rm", "-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
			"-e", "POSTGRES_PASSWORD=openadr", "-e", "POSTGRES_DB=openadr", image).CombinedOutput()
		if err != nil {
			serverFail = fmt.Sprintf("no OPENADR_POSTGRES_DSN, and Docker cannot start %s: %v: %s", image, err, strings.TrimSpace(string(out)))
			return
		}
		container = strings.TrimSpace(string(out))
		candidate := fmt.Sprintf("postgres://postgres:openadr@127.0.0.1:%d/openadr?sslmode=disable", port)
		// The server starts, stops and starts again while it sets itself
		// up: it is ready when it has answered for a little while.
		deadline, answered := time.Now().Add(90*time.Second), 0
		for answered < 3 {
			if time.Now().After(deadline) {
				serverFail = "the PostgreSQL container did not come up"
				return
			}
			time.Sleep(300 * time.Millisecond)
			db, err := sql.Open("pgx", candidate)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if db.PingContext(ctx) == nil {
				answered++
			} else {
				answered = 0
			}
			cancel()
			_ = db.Close()
		}
		serverDSN = candidate
	})
	if serverDSN == "" {
		if os.Getenv("OPENADR_STORAGE_REQUIRE") != "" {
			t.Fatal(serverFail)
		}
		t.Skip(serverFail)
	}
	return serverDSN
}

var container string

func TestMain(m *testing.M) {
	code := m.Run()
	if container != "" {
		_ = exec.Command("docker", "rm", "-f", container).Run()
	}
	os.Exit(code)
}

// schemasOf remembers the schema each store of a test lives in: a schema
// of its own per store is what makes a store empty.
type schemasOf map[storage.Store]string

func (m schemasOf) open(t *testing.T, schema string) storage.Store {
	t.Helper()
	base := dsn(t)
	ctx := context.Background()
	if schema == "" {
		schema = fmt.Sprintf("openadr_test_%d_%d", os.Getpid(), schemas.Add(1))
		admin, err := sql.Open("pgx", base)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
			t.Fatal(err)
		}
		_ = admin.Close()
		t.Cleanup(func() {
			admin, err := sql.Open("pgx", base)
			if err == nil {
				_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
				_ = admin.Close()
			}
		})
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	s, err := postgres.Open(ctx, base+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m[s] = schema
	return s
}

func (m schemasOf) reopen(t *testing.T, s storage.Store) storage.Store {
	t.Helper()
	if err := s.(*sqlstore.Store).Close(); err != nil {
		t.Fatal(err)
	}
	return m.open(t, m[s])
}

// The suite every store of a go-openadr VTN is held to.
func TestStore(t *testing.T) {
	m := schemasOf{}
	storagetest.Run(t, storagetest.Config{
		New:    func(t *testing.T) storage.Store { return m.open(t, "") },
		Reopen: m.reopen,
	})
}

// A VTN on the store, driven by clients, across a restart.
func TestVTN(t *testing.T) {
	m := schemasOf{}
	vtntest.Workflow(t, m.open(t, ""), m.reopen)
}

// Two stores on one database, as two VTN processes would be: the rules
// hold between them.
func TestTwoProcesses(t *testing.T) {
	m := schemasOf{}
	storagetest.Run(t, storagetest.Config{New: func(t *testing.T) storage.Store {
		// A fresh schema for each part of the suite, with two stores on
		// it that take turns.
		a := m.open(t, "")
		return alternate{a, m.open(t, m[a])}
	}})
}

// alternate sends every other call to another store on the same database.
type alternate struct{ a, b storage.Store }

var turn atomic.Int64

func (s alternate) pick() storage.Store {
	if turn.Add(1)%2 == 0 {
		return s.a
	}
	return s.b
}

func (s alternate) Programs() storage.Collection[openadrProgram, storage.ProgramFilter] {
	return s.pick().Programs()
}
func (s alternate) Events() storage.Collection[openadrEvent, storage.EventFilter] {
	return s.pick().Events()
}
func (s alternate) Reports() storage.Collection[openadrReport, storage.ReportFilter] {
	return s.pick().Reports()
}
func (s alternate) VENs() storage.Collection[openadrVEN, storage.VENFilter] { return s.pick().VENs() }
func (s alternate) Resources() storage.Collection[openadrResource, storage.ResourceFilter] {
	return s.pick().Resources()
}
func (s alternate) Subscriptions() storage.Collection[storage.SubscriptionRecord, storage.SubscriptionFilter] {
	return s.pick().Subscriptions()
}
func (s alternate) ResourceGroups() storage.Collection[openadrResourceGroup, storage.ResourceGroupFilter] {
	return s.pick().ResourceGroups()
}
