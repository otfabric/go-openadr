// SPDX-License-Identifier: MIT

// Package sqlite opens a storage.Store for the VTN of go-openadr on a
// SQLite database: one file, no server, which is what a gateway or a small
// VTN wants.
//
// It uses modernc.org/sqlite, a SQLite that needs no C compiler. Nothing
// of it is a dependency of go-openadr: this example is a module of its own.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // the database/sql driver "sqlite"

	"github.com/otfabric/go-openadr/examples/storage/sqlstore"
)

// Dialect is SQLite to package sqlstore.
var Dialect = sqlstore.Dialect{
	Name:   "SQLite",
	Serial: "INTEGER PRIMARY KEY AUTOINCREMENT",
	JSON:   "TEXT",
	// No lock statement: the store has one connection, so one transaction
	// at a time is all there can be.
}

// Open opens the database in the file at path, creating it when it is not
// there.
func Open(ctx context.Context, path string) (*sqlstore.Store, error) {
	// References are checked, a write waits for another process rather
	// than fail, and the write-ahead log keeps a crash from costing more
	// than the transaction it interrupted.
	dsn := "file:" + url.PathEscape(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	// SQLite has one writer. One connection makes the Go side agree with
	// that, and makes the checks of a write and the write one step.
	db.SetMaxOpenConns(1)
	store, err := sqlstore.New(ctx, db, Dialect)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}
