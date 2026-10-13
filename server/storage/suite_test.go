// SPDX-License-Identifier: MIT

package storage_test

import (
	"path/filepath"
	"testing"

	"github.com/otfabric/go-openadr/server/storage"
	"github.com/otfabric/go-openadr/server/storage/storagetest"
)

// The stores of this module, against the suite every store is held to.

func TestMemorySuite(t *testing.T) {
	storagetest.Run(t, storagetest.Config{New: func(*testing.T) storage.Store { return storage.NewMemory() }})
}

func TestFileSuite(t *testing.T) {
	paths := map[storage.Store]string{}
	open := func(t *testing.T, path string) storage.Store {
		t.Helper()
		f, err := storage.OpenFile(path, storage.FileOptions{NoSync: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		paths[f] = path
		return f
	}
	storagetest.Run(t, storagetest.Config{
		New: func(t *testing.T) storage.Store { return open(t, filepath.Join(t.TempDir(), "journal")) },
		Reopen: func(t *testing.T, s storage.Store) storage.Store {
			if err := s.(*storage.File).Close(); err != nil {
				t.Fatal(err)
			}
			return open(t, paths[s])
		},
	})
}
