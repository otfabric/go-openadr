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
	// owner is the client that keeps v, for quota; nil for a kind that
	// is not limited per client.
	owner func(v T) string
	quota quota
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
		if err == nil {
			v, err = k.admitNew(r.Context(), s, id, v, form)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		k.notify(s, r, openadr.OperationCreate, v)
		writeObject(s, w, r, http.StatusCreated, k.codec, v)
	}
}

// admitNew admits and stores a new object. It holds s.writing, so that
// what admit looked up (the ven of a resource) is still so when the object
// is stored.
func (k kind[T, F]) admitNew(ctx context.Context, s *Server, id auth.Identity, v T, form wire.Form) (T, error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	var err error
	if k.admit != nil {
		if v, err = k.admit(ctx, id, v, form, nil); err != nil {
			return v, err
		}
	}
	meta, err := s.created()
	if err != nil {
		return v, err
	}
	*k.meta(&v) = meta
	return v, k.store(ctx, s, v)
}

// store creates v, within the quota of the client that keeps it.
func (k kind[T, F]) store(ctx context.Context, s *Server, v T) error {
	if k.owner == nil || k.quota.max <= 0 {
		return k.coll.Create(ctx, v)
	}
	s.creating.Lock()
	defer s.creating.Unlock()
	if err := k.quota.admit(ctx, k.owner(v)); err != nil {
		return err
	}
	return k.coll.Create(ctx, v)
}

func update[T interface{ Validate() error }, F any](s *Server, k kind[T, F]) handler {
	return func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		// The body is read before the stored object is looked at: what
		// the update keeps of the stored object (the targets of a VEN's
		// ven, say) is then what is stored when it is replaced, however
		// long the client takes to send the body.
		v, form, err := readObject(s, w, r, k.codec)
		if err == nil {
			v, err = k.replace(r, s, id, v, form)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		k.notify(s, r, openadr.OperationUpdate, v)
		writeObject(s, w, r, http.StatusOK, k.codec, v)
	}
}

// replace finds the object named in the path, admits v in its place and
// stores it. It holds s.writing throughout, so that no other write comes
// between what admit read and the update: of two clients that replace the
// same object, the one that comes last wins.
func (k kind[T, F]) replace(r *http.Request, s *Server, id auth.Identity, v T, form wire.Form) (T, error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	old, err := k.find(r, id)
	if err != nil {
		return v, err
	}
	if k.admit != nil {
		if v, err = k.admit(r.Context(), id, v, form, &old); err != nil {
			return v, err
		}
	}
	*k.meta(&v) = s.modified(*k.meta(&old))
	return v, k.coll.Update(r.Context(), v)
}

func remove[T interface{ Validate() error }, F any](s *Server, k kind[T, F]) handler {
	return func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		v, err := k.delete(r, s, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		k.notify(s, r, openadr.OperationDelete, v)
		writeObject(s, w, r, http.StatusOK, k.codec, v)
	}
}

// delete deletes the object named in the path if the client may see it,
// under s.writing like replace: a VEN cannot delete a ven that Business
// Logic has just given to another client.
func (k kind[T, F]) delete(r *http.Request, s *Server, id auth.Identity) (T, error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	v, err := k.find(r, id)
	if err != nil {
		return v, err
	}
	return k.coll.Delete(r.Context(), k.meta(&v).ID)
}
