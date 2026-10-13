// SPDX-License-Identifier: MIT

package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/otfabric/go-openadr/examples/storage/internal/vtntest"
	"github.com/otfabric/go-openadr/examples/storage/sqlite"
	"github.com/otfabric/go-openadr/examples/storage/sqlstore"
	"github.com/otfabric/go-openadr/server/storage"
	"github.com/otfabric/go-openadr/server/storage/storagetest"
)

// files remembers where each store of a test keeps its database.
type files map[storage.Store]string

func (f files) open(t *testing.T, path string) storage.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	f[s] = path
	return s
}

func (f files) reopen(t *testing.T, s storage.Store) storage.Store {
	t.Helper()
	if err := s.(*sqlstore.Store).Close(); err != nil {
		t.Fatal(err)
	}
	return f.open(t, f[s])
}

// The suite every store of a go-openadr VTN is held to.
func TestStore(t *testing.T) {
	f := files{}
	storagetest.Run(t, storagetest.Config{
		New:    func(t *testing.T) storage.Store { return f.open(t, filepath.Join(t.TempDir(), "vtn.db")) },
		Reopen: f.reopen,
	})
}

// A VTN on the store, driven by clients, across a restart.
func TestVTN(t *testing.T) {
	f := files{}
	// A path that needs escaping in a URL, which is how SQLite is told it.
	vtntest.Workflow(t, f.open(t, filepath.Join(t.TempDir(), "my vtn #1?.db")), f.reopen)
}
