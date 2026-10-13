// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"net/http"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/internal/wire"
	"github.com/otfabric/go-openadr/server/storage"
)

// kind describes one type of object to the operations all types share:
// read, create, update and delete by ID. Searches differ per type and are
// written out in handlers.go.
type kind[T interface{ Validate() error }, F any] struct {
	codec wire.ObjectCodec[T]
	coll  storage.Collection[T, F]
	meta  func(*T) *openadr.Metadata
	// see returns v as the client may see it, or false if the client may
	// not know of it; the client is then told it does not exist.
	see func(ctx context.Context, id auth.Identity, v T) (T, bool, error)
	// admit applies the access rules to a create request, or to an update
	// of old, and returns the object to store. Nil admits v as it is.
	admit func(ctx context.Context, id auth.Identity, v T, form wire.Form, old *T) (T, error)
	// subject describes v to the notifications; see notify. Nil for a
	// kind of object that nobody is notified of.
	subject func(ctx context.Context, v T) subject
}

func (k kind[T, F]) notify(s *Server, r *http.Request, op openadr.Operation, v T) {
	if k.subject != nil {
		s.notify(r.Context(), op, k.subject(r.Context(), v))
	}
}

// find loads the object named in the path, if the client may see it.
func (k kind[T, F]) find(r *http.Request, id auth.Identity) (T, error) {
	var zero T
	objectID, err := pathID(r)
	if err != nil {
		return zero, err
	}
	v, err := k.coll.Get(r.Context(), objectID)
	if err != nil {
		return zero, err
	}
	v, ok, err := k.see(r.Context(), id, v)
	if err != nil {
		return zero, err
	}
	if !ok {
		return zero, openadr.ErrNotFound
	}
	return v, nil
}

func get[T interface{ Validate() error }, F any](s *Server, k kind[T, F]) handler {
	return func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		v, err := k.find(r, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		k.notifyRead(s, r, k.meta(&v).ID)
		writeObject(s, w, r, http.StatusOK, k.codec, v)
	}
}

// notifyRead tells the subscribers to READ that the objects with the given
// IDs were read, on a VTN that does that. Each is notified of the object
// as it may see it itself, which is not always as the reader saw it, so
// the objects are taken from the store again.
func (k kind[T, F]) notifyRead(s *Server, r *http.Request, ids ...string) {
	if !s.reads || k.subject == nil {
		return
	}
	for _, id := range ids {
		if v, err := k.coll.Get(r.Context(), id); err == nil {
			s.notify(r.Context(), openadr.OperationRead, k.subject(r.Context(), v))
		}
	}
}

// readPage is notifyRead for the page of a search that is about to be
// written.
func readPage[T interface{ Validate() error }, F any](s *Server, r *http.Request, k kind[T, F], items []T, p page) {
	if !s.reads || k.subject == nil {
		return
	}
	shown := paginate(items, p)
	ids := make([]string, len(shown))
	for i := range shown {
		ids[i] = k.meta(&shown[i]).ID
	}
	k.notifyRead(s, r, ids...)
}

func create[T interface{ Validate() error }, F any](s *Server, k kind[T, F]) handler {
	return func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		v, form, err := readObject(s, w, r, k.codec)
		if err == nil && k.admit != nil {
			v, err = k.admit(r.Context(), id, v, form, nil)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		meta, err := s.created()
		if err != nil {
			s.fail(w, r, err)
			return
		}
		*k.meta(&v) = meta
		if err := k.coll.Create(r.Context(), v); err != nil {
			s.fail(w, r, err)
			return
		}
		k.notify(s, r, openadr.OperationCreate, v)
		writeObject(s, w, r, http.StatusCreated, k.codec, v)
	}
}

func update[T interface{ Validate() error }, F any](s *Server, k kind[T, F]) handler {
	return func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		old, err := k.find(r, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		v, form, err := readObject(s, w, r, k.codec)
		if err == nil && k.admit != nil {
			v, err = k.admit(r.Context(), id, v, form, &old)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		*k.meta(&v) = s.modified(*k.meta(&old))
		if err := k.coll.Update(r.Context(), v); err != nil {
			s.fail(w, r, err)
			return
		}
		k.notify(s, r, openadr.OperationUpdate, v)
		writeObject(s, w, r, http.StatusOK, k.codec, v)
	}
}

func remove[T interface{ Validate() error }, F any](s *Server, k kind[T, F]) handler {
	return func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		v, err := k.find(r, id)
		if err == nil {
			v, err = k.coll.Delete(r.Context(), k.meta(&v).ID)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		k.notify(s, r, openadr.OperationDelete, v)
		writeObject(s, w, r, http.StatusOK, k.codec, v)
	}
}
