// SPDX-License-Identifier: MIT

package storage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
	v310 "github.com/otfabric/go-openadr/internal/wire/v310"
)

// File is a Store that survives a restart: it keeps everything in memory,
// like Memory, and writes every change to a journal file before the write
// returns. Opening the file replays the journal.
//
// It is meant for a standalone VTN with a modest number of objects: all of
// them are held in memory, and the file is read in full at start. One
// process uses a file at a time; File does not lock it against a second.
//
// A File is safe for concurrent use. Close it when done.
type File struct {
	mem *Memory

	// mu orders the writes: a change is applied to memory and appended to
	// the journal as one step, so that the journal replays to the same state.
	mu     sync.Mutex
	path   string
	file   *os.File
	sync   bool
	broken error
}

// FileOptions adjusts OpenFile.
type FileOptions struct {
	// NoSync skips the fsync after each change. Changes then survive a
	// crash of the process, and may be lost in a crash of the machine.
	NoSync bool
}

// record is one line of the journal: a change to one object.
type record struct {
	Op   string `json:"op"` // create, update or delete
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	// Object is the object in the JSON of OpenADR 3.1.0, as the API shows
	// it to its owner. Absent for a delete.
	Object json.RawMessage `json:"object,omitempty"`
	// ReadsAll belongs to a subscription; see SubscriptionRecord.
	ReadsAll bool `json:"readsAll,omitempty"`
	// Delivery belongs to the kind "delivery": a notification on its way.
	Delivery *storedDelivery `json:"delivery,omitempty"`
}

// storedDelivery is a Delivery in the journal. The body is JSON already,
// and is kept as it is.
type storedDelivery struct {
	Subscription string          `json:"subscription"`
	URL          string          `json:"url"`
	Token        string          `json:"token,omitempty"`
	Body         json.RawMessage `json:"body"`
	Created      time.Time       `json:"created"`
}

var fileCodec = v310.Codec()

// ErrCorrupt reports a journal that cannot be replayed.
var ErrCorrupt = errors.New("storage: the journal is corrupt")

// ErrReadOnly is matched by every write to a File after one that could
// not be written to the journal: the store then accepts no change until it
// is opened again, so that memory is never more than one change ahead of
// the file. Reads go on.
var ErrReadOnly = errors.New("storage: the store is read-only")

// OpenFile opens the store kept in the file at path, creating it when it
// does not exist. The journal is replayed and then rewritten without the
// changes that later ones made obsolete.
//
// A last line that was cut short by a crash is dropped: the change it held
// had not been confirmed to anyone. Damage anywhere else is ErrCorrupt.
func OpenFile(path string, opts FileOptions) (*File, error) {
	f := &File{mem: NewMemory(), path: path, sync: !opts.NoSync}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := f.replay(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.compact(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *File) replay(data []byte) error {
	ctx := context.Background()
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 64<<20)
	complete := bytes.HasSuffix(data, []byte("\n"))
	lines := bytes.Count(data, []byte("\n"))
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec record
		err := json.Unmarshal(line, &rec)
		if err == nil {
			err = f.apply(ctx, rec)
		}
		if err != nil {
			if !complete && n == lines+1 {
				// The unfinished last line of a crash.
				return nil
			}
			return fmt.Errorf("%w: line %d: %v", ErrCorrupt, n, err)
		}
	}
	return scanner.Err()
}

// apply replays one record into memory.
func (f *File) apply(ctx context.Context, rec record) error {
	switch rec.Kind {
	case "program":
		return applyTo(ctx, f.mem.Programs(), fileCodec.Programs, rec)
	case "event":
		return applyTo(ctx, f.mem.Events(), fileCodec.Events, rec)
	case "report":
		return applyTo(ctx, f.mem.Reports(), fileCodec.Reports, rec)
	case "orphan-report":
		// A report that outlived its event, from a snapshot.
		rep, _, err := fileCodec.Reports.Unmarshal(rec.Object)
		if err != nil {
			return err
		}
		return f.mem.restoreReport(rep)
	case "ven":
		return applyTo(ctx, f.mem.VENs(), fileCodec.VENs, rec)
	case "resource":
		return applyTo(ctx, f.mem.Resources(), fileCodec.Resources, rec)
	case "resource-group":
		return applyTo(ctx, f.mem.ResourceGroups(), fileCodec.ResourceGroups, rec)
	case "restored-resource-group":
		// From a snapshot: its children may come later in the file.
		g, _, err := fileCodec.ResourceGroups.Unmarshal(rec.Object)
		if err != nil {
			return err
		}
		return f.mem.restoreResourceGroup(g)
	case "delivery":
		if rec.Op == "delete" {
			return f.mem.RemoveDelivery(ctx, rec.ID)
		}
		if rec.Delivery == nil {
			return errors.New("a delivery without its content")
		}
		d := rec.Delivery
		return f.mem.AddDelivery(ctx, Delivery{ID: rec.ID, SubscriptionID: d.Subscription, CallbackURL: d.URL, BearerToken: d.Token, Body: d.Body, Created: d.Created})
	case "subscription":
		if rec.Op == "delete" {
			_, err := f.mem.Subscriptions().Delete(ctx, rec.ID)
			return err
		}
		sub, _, err := fileCodec.Subscriptions.Unmarshal(rec.Object)
		if err != nil {
			return err
		}
		s := SubscriptionRecord{Subscription: sub, ReadsAll: rec.ReadsAll}
		if rec.Op == "create" {
			return f.mem.Subscriptions().Create(ctx, s)
		}
		return f.mem.Subscriptions().Update(ctx, s)
	}
	return fmt.Errorf("unknown kind %q", rec.Kind)
}

func applyTo[T, F any](ctx context.Context, c Collection[T, F], codec wire.ObjectCodec[T], rec record) error {
	switch rec.Op {
	case "delete":
		_, err := c.Delete(ctx, rec.ID)
		return err
	case "create", "update":
		v, _, err := codec.Unmarshal(rec.Object)
		if err != nil {
			return err
		}
		if rec.Op == "create" {
			return c.Create(ctx, v)
		}
		return c.Update(ctx, v)
	}
	return fmt.Errorf("unknown operation %q", rec.Op)
}

// snapshot returns the state as the shortest journal that replays to it:
// one create per object, parents before what refers to them.
func (f *File) snapshot() ([]byte, error) {
	ctx := context.Background()
	var out bytes.Buffer
	add := func(rec record, object []byte, err error) error {
		if err != nil {
			return err
		}
		rec.Op, rec.Object = "create", object
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		out.Write(line)
		out.WriteByte('\n')
		return nil
	}
	programs, _ := f.mem.Programs().List(ctx, ProgramFilter{})
	for _, v := range programs {
		b, err := fileCodec.Programs.Marshal(v, wire.Object)
		if err := add(record{Kind: "program"}, b, err); err != nil {
			return nil, err
		}
	}
	events, _ := f.mem.Events().List(ctx, EventFilter{})
	for _, v := range events {
		b, err := fileCodec.Events.Marshal(v, wire.Object)
		if err := add(record{Kind: "event"}, b, err); err != nil {
			return nil, err
		}
	}
	vens, _ := f.mem.VENs().List(ctx, VENFilter{})
	for _, v := range vens {
		b, err := fileCodec.VENs.Marshal(v, wire.Object)
		if err := add(record{Kind: "ven"}, b, err); err != nil {
			return nil, err
		}
	}
	resources, _ := f.mem.Resources().List(ctx, ResourceFilter{})
	for _, v := range resources {
		b, err := fileCodec.Resources.Marshal(v, wire.Object)
		if err := add(record{Kind: "resource"}, b, err); err != nil {
			return nil, err
		}
	}
	groups, _ := f.mem.ResourceGroups().List(ctx, ResourceGroupFilter{})
	for _, v := range groups {
		b, err := fileCodec.ResourceGroups.Marshal(v, wire.Object)
		if err := add(record{Kind: "restored-resource-group"}, b, err); err != nil {
			return nil, err
		}
	}
	subscriptions, _ := f.mem.Subscriptions().List(ctx, SubscriptionFilter{})
	for _, v := range subscriptions {
		b, err := fileCodec.Subscriptions.Marshal(v.Subscription, wire.Object)
		if err := add(record{Kind: "subscription", ReadsAll: v.ReadsAll}, b, err); err != nil {
			return nil, err
		}
	}
	deliveries, _ := f.mem.Deliveries(ctx)
	for _, d := range deliveries {
		if err := add(deliveryRecord(d), nil, nil); err != nil {
			return nil, err
		}
	}
	// Reports last. One whose event is gone cannot be created by a replay,
	// which checks references: it is written as its own kind of record.
	reports, _ := f.mem.Reports().List(ctx, ReportFilter{})
	for _, v := range reports {
		b, err := fileCodec.Reports.Marshal(v, wire.Object)
		kind := "report"
		if _, gone := f.mem.Events().Get(ctx, v.EventID); gone != nil {
			kind = "orphan-report"
		}
		if err := add(record{Kind: kind}, b, err); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

// compact replaces the journal by a snapshot and opens it for appending.
// The new file is complete on disk before it takes the place of the old.
func (f *File) compact() error {
	data, err := f.snapshot()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), filepath.Base(f.path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), f.path)
	}
	if err != nil {
		return err
	}
	if dir, derr := os.Open(filepath.Dir(f.path)); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	f.file, err = os.OpenFile(f.path, os.O_WRONLY|os.O_APPEND, 0o600)
	return err
}

// write appends one record. The caller holds f.mu and has applied the change
// to memory.
func (f *File) write(rec record) error {
	line, err := json.Marshal(rec)
	if err == nil {
		_, err = f.file.Write(append(line, '\n'))
	}
	if err == nil && f.sync {
		err = f.file.Sync()
	}
	if err != nil {
		// Memory now holds a change the file does not. Reads go on; no
		// further change is accepted, so that the two cannot drift apart
		// by more than this one.
		f.broken = fmt.Errorf("%w until restarted: the journal could not be written: %w", ErrReadOnly, err)
		return f.broken
	}
	return nil
}

// Close flushes and closes the journal. The store must not be used after.
func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	err := f.file.Sync()
	if cerr := f.file.Close(); err == nil {
		err = cerr
	}
	f.file = nil
	if f.broken == nil {
		f.broken = fmt.Errorf("%w: the store", openadr.ErrClosed)
	}
	return err
}

// journaled is a collection of a File: reads come from memory, writes go to
// memory and to the journal.
type journaled[T, F any] struct {
	f       *File
	inner   Collection[T, F]
	kind    string
	marshal func(T) (record, error)
}

func (c journaled[T, F]) change(op string, id string, v *T, do func() error) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.f.broken != nil {
		return c.f.broken
	}
	rec := record{Kind: c.kind, ID: id}
	if v != nil {
		var err error
		if rec, err = c.marshal(*v); err != nil {
			return err
		}
		rec.Kind = c.kind
	}
	rec.Op = op
	if err := do(); err != nil {
		return err
	}
	return c.f.write(rec)
}

func (c journaled[T, F]) Create(ctx context.Context, v T) error {
	return c.change("create", "", &v, func() error { return c.inner.Create(ctx, v) })
}

func (c journaled[T, F]) Update(ctx context.Context, v T) error {
	return c.change("update", "", &v, func() error { return c.inner.Update(ctx, v) })
}

func (c journaled[T, F]) Delete(ctx context.Context, id string) (T, error) {
	var deleted T
	err := c.change("delete", id, nil, func() (err error) {
		deleted, err = c.inner.Delete(ctx, id)
		return err
	})
	return deleted, err
}

func (c journaled[T, F]) Get(ctx context.Context, id string) (T, error) { return c.inner.Get(ctx, id) }

func (c journaled[T, F]) List(ctx context.Context, f F) ([]T, error) { return c.inner.List(ctx, f) }

func object[T any](codec wire.ObjectCodec[T]) func(T) (record, error) {
	return func(v T) (record, error) {
		b, err := codec.Marshal(v, wire.Object)
		return record{Object: b}, err
	}
}

// Programs implements Store.
func (f *File) Programs() Collection[openadr.Program, ProgramFilter] {
	return journaled[openadr.Program, ProgramFilter]{f: f, inner: f.mem.Programs(), kind: "program", marshal: object(fileCodec.Programs)}
}

// Events implements Store.
func (f *File) Events() Collection[openadr.Event, EventFilter] {
	return journaled[openadr.Event, EventFilter]{f: f, inner: f.mem.Events(), kind: "event", marshal: object(fileCodec.Events)}
}

// Reports implements Store.
func (f *File) Reports() Collection[openadr.Report, ReportFilter] {
	return journaled[openadr.Report, ReportFilter]{f: f, inner: f.mem.Reports(), kind: "report", marshal: object(fileCodec.Reports)}
}

// VENs implements Store.
func (f *File) VENs() Collection[openadr.VEN, VENFilter] {
	return journaled[openadr.VEN, VENFilter]{f: f, inner: f.mem.VENs(), kind: "ven", marshal: object(fileCodec.VENs)}
}

// Resources implements Store.
func (f *File) Resources() Collection[openadr.Resource, ResourceFilter] {
	return journaled[openadr.Resource, ResourceFilter]{f: f, inner: f.mem.Resources(), kind: "resource", marshal: object(fileCodec.Resources)}
}

// ResourceGroups implements Store.
func (f *File) ResourceGroups() Collection[openadr.ResourceGroup, ResourceGroupFilter] {
	return journaled[openadr.ResourceGroup, ResourceGroupFilter]{f: f, inner: f.mem.ResourceGroups(), kind: "resource-group", marshal: object(fileCodec.ResourceGroups)}
}

// Subscriptions implements Store.
func (f *File) Subscriptions() Collection[SubscriptionRecord, SubscriptionFilter] {
	return journaled[SubscriptionRecord, SubscriptionFilter]{
		f: f, inner: f.mem.Subscriptions(), kind: "subscription",
		marshal: func(s SubscriptionRecord) (record, error) {
			b, err := fileCodec.Subscriptions.Marshal(s.Subscription, wire.Object)
			return record{Object: b, ReadsAll: s.ReadsAll}, err
		},
	}
}

func deliveryRecord(d Delivery) record {
	return record{Kind: "delivery", ID: d.ID, Delivery: &storedDelivery{
		Subscription: d.SubscriptionID, URL: d.CallbackURL, Token: d.BearerToken, Body: d.Body, Created: d.Created,
	}}
}

// outboxChange is a change to the outbox: applied to memory, then written.
func (f *File) outboxChange(rec record, do func() error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.broken != nil {
		return f.broken
	}
	if err := do(); err != nil {
		return err
	}
	return f.write(rec)
}

// AddDelivery implements Outbox: the notification is in the journal, and
// with FileOptions as they are by default on the disk, before it returns.
func (f *File) AddDelivery(ctx context.Context, d Delivery) error {
	if !json.Valid(d.Body) {
		return errors.New("storage: the body of a delivery is not JSON")
	}
	rec := deliveryRecord(d)
	rec.Op = "create"
	return f.outboxChange(rec, func() error { return f.mem.AddDelivery(ctx, d) })
}

// RemoveDelivery implements Outbox.
func (f *File) RemoveDelivery(ctx context.Context, id string) error {
	return f.outboxChange(record{Op: "delete", Kind: "delivery", ID: id}, func() error { return f.mem.RemoveDelivery(ctx, id) })
}

// Deliveries implements Outbox.
func (f *File) Deliveries(ctx context.Context) ([]Delivery, error) { return f.mem.Deliveries(ctx) }
