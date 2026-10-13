// SPDX-License-Identifier: MIT

// Package postgres opens a storage.Store for the VTN of go-openadr on a
// PostgreSQL database, for a VTN that shares its data with other services
// or runs as more than one process.
//
// It uses github.com/jackc/pgx/v5 through database/sql. Nothing of it is a
// dependency of go-openadr: this example is a module of its own.
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // the database/sql driver "pgx"

	"github.com/otfabric/go-openadr/examples/storage/sqlstore"
)

// Dialect is PostgreSQL to package sqlstore.
var Dialect = sqlstore.Dialect{
	Name:     "PostgreSQL",
	Serial:   "BIGSERIAL PRIMARY KEY",
	JSON:     "JSONB",
	Numbered: true,
	// Writers take turns, also those of other processes on the same
	// database: the lock is the database's, and is given up with the
	// transaction. The number is arbitrary and this store's own.
	Lock: "SELECT pg_advisory_xact_lock(7230317)",
}

// Open connects to the database named by a connection string, such as
// postgres://user:password@host:5432/dbname?sslmode=require, and creates
// the tables of the store in it when they are not there.
func Open(ctx context.Context, dsn string) (*sqlstore.Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: %w", err)
	}
	store, err := sqlstore.New(ctx, db, Dialect)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}
