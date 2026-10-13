// SPDX-License-Identifier: MIT

// Package sqlstore is a storage.Store on a SQL database, written against
// database/sql so that the same code runs on SQLite and on PostgreSQL; the
// packages sqlite and postgres beside it open one of each.
//
// It is an example of bringing your own storage to the VTN of go-openadr,
// and it is meant to be read and copied. What it shows:
//
//   - each object is kept as one JSON document (storage.Encode), beside the
//     few columns the VTN searches by, so the schema does not follow every
//     property of OpenADR;
//   - the rules a Store has to enforce (unique names, references that lead
//     somewhere, what may not be deleted) are checked inside the
//     transaction of the write, and the database's own constraints stand
//     behind them;
//   - a violation is a *storage.ConstraintError, which the VTN answers
//     with 409, and anything else is the database's error, which it
//     answers with 500;
//   - the result of a search is in the order of creation;
//   - the store is also a storage.Outbox, so that a VTN on it keeps its
//     webhook notifications until they are delivered.
//
// Writers take turns: a write transaction begins by taking one lock for
// the whole store. That keeps the checks simple and right, and costs
// concurrent writes, which a VTN has few of. A store for a VTN with many
// writers would rely on the constraints of the database instead and
// translate its errors.
//
// The package passes storagetest.Run of go-openadr on both databases.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/server/storage"
)

// Dialect is what differs between the databases.
type Dialect struct {
	// Name is for error messages.
	Name string
	// Serial is the type of a column that numbers rows as they are
	// inserted, and is the primary key.
	Serial string
	// JSON is the type of a column that holds a JSON document.
	JSON string
	// Numbered says the placeholders are $1, $2, ... and not ?.
	Numbered bool
	// Lock is a statement that makes a write transaction wait for the one
	// before it, where the database does not do that by itself.
	Lock string
}

// Store is a storage.Store on a SQL database.
type Store struct {
	db      *sql.DB
	dialect Dialect
}

var (
	_ storage.Store  = (*Store)(nil)
	_ storage.Outbox = (*Store)(nil)
)

// New returns a Store on db, creating its tables when they are not there.
// The Store closes db when it is closed.
func New(ctx context.Context, db *sql.DB, d Dialect) (*Store, error) {
	s := &Store{db: db, dialect: d}
	for _, stmt := range schema(d) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("sqlstore: creating the %s schema: %w", d.Name, err)
		}
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// schema returns the statements that create the tables. Each table has the
// ID of the object, a number that keeps the order of creation, the columns
// the VTN filters and the rules need, and the object itself as JSON.
func schema(d Dialect) []string {
	table := func(name, columns string) string {
		return "CREATE TABLE IF NOT EXISTS " + name + " (seq " + d.Serial + ", id TEXT NOT NULL UNIQUE, body " + d.JSON + " NOT NULL, " + columns + ")"
	}
	return []string{
		table("programs", "name TEXT NOT NULL UNIQUE"),
		table("events", "program_id TEXT NOT NULL REFERENCES programs (id)"),
		// No reference to events: a report outlives its event.
		table("reports", "event_id TEXT NOT NULL, client_id TEXT NOT NULL, client_name TEXT NOT NULL"),
		table("vens", "name TEXT NOT NULL UNIQUE, client_id TEXT NOT NULL"),
		table("resources", "ven_id TEXT NOT NULL REFERENCES vens (id), name TEXT NOT NULL, client_id TEXT NOT NULL, UNIQUE (ven_id, name)"),
		table("subscriptions", "program_id TEXT NOT NULL, client_id TEXT NOT NULL, client_name TEXT NOT NULL"),
		table("resource_groups", "name TEXT NOT NULL"),
		// Who holds what, to find the groups a deleted object has to leave.
		"CREATE TABLE IF NOT EXISTS resource_group_children (group_id TEXT NOT NULL, child_type TEXT NOT NULL, child_id TEXT NOT NULL)",
		// The outbox: notifications on their way to webhooks.
		"CREATE TABLE IF NOT EXISTS deliveries (seq " + d.Serial + ", id TEXT NOT NULL UNIQUE, subscription_id TEXT NOT NULL, callback_url TEXT NOT NULL, bearer_token TEXT NOT NULL, body " + d.JSON + " NOT NULL, created TEXT NOT NULL)",
		"CREATE INDEX IF NOT EXISTS events_program ON events (program_id)",
		"CREATE INDEX IF NOT EXISTS reports_event ON reports (event_id)",
		"CREATE INDEX IF NOT EXISTS reports_client ON reports (client_id)",
		"CREATE INDEX IF NOT EXISTS vens_client ON vens (client_id)",
		"CREATE INDEX IF NOT EXISTS resources_client ON resources (client_id)",
		"CREATE INDEX IF NOT EXISTS subscriptions_client ON subscriptions (client_id)",
		"CREATE INDEX IF NOT EXISTS resource_group_children_child ON resource_group_children (child_type, child_id)",
	}
}

// q writes a query for the dialect: ? becomes $1, $2, ... where needed.
func (s *Store) q(query string) string {
	if !s.dialect.Numbered {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// write runs fn in a transaction that no other write runs beside.
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// After a commit this does nothing.
	defer func() { _ = tx.Rollback() }()
	if s.dialect.Lock != "" {
		if _, err := tx.ExecContext(ctx, s.dialect.Lock); err != nil {
			return err
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// exists reports whether a query has a row.
func (s *Store) exists(ctx context.Context, tx *sql.Tx, query string, args ...any) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, s.q("SELECT 1 WHERE EXISTS ("+query+")"), args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func conflict(format string, args ...any) error {
	return &storage.ConstraintError{Detail: fmt.Sprintf(format, args...)}
}

// column is one of the columns of a table beside id and body.
type column struct {
	name  string
	value any
}

// table implements storage.Collection on one table.
type table[T storage.Object, F any] struct {
	s    *Store
	name string
	id   func(T) string
	// columns returns what is kept beside the document, to search by.
	columns func(T) []column
	// check validates a create, or an update of old, inside the write.
	check func(ctx context.Context, tx *sql.Tx, v T, old *T) error
	// canDelete vetoes a delete, and deleted follows one.
	canDelete func(ctx context.Context, tx *sql.Tx, v T) error
	deleted   func(ctx context.Context, tx *sql.Tx, v T) error
	// stored follows a create or an update.
	stored func(ctx context.Context, tx *sql.Tx, v T) error
	// where turns a filter into conditions.
	where func(F) (conditions []string, args []any)
}

// load reads one object. q is the database or a transaction.
func (t table[T, F]) load(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (T, error) {
	var (
		zero T
		body []byte
	)
	err := q.QueryRowContext(ctx, t.s.q("SELECT body FROM "+t.name+" WHERE id = ?"), id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, openadr.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	return storage.Decode[T](body)
}

func (t table[T, F]) Get(ctx context.Context, id string) (T, error) {
	return t.load(ctx, t.s.db, id)
}

func (t table[T, F]) Create(ctx context.Context, v T) error {
	body, err := storage.Encode(v)
	if err != nil {
		return err
	}
	return t.s.write(ctx, func(tx *sql.Tx) error {
		id := t.id(v)
		if taken, err := t.s.exists(ctx, tx, "SELECT 1 FROM "+t.name+" WHERE id = ?", id); err != nil {
			return err
		} else if taken {
			return conflict("ID %s is in use", id)
		}
		if t.check != nil {
			if err := t.check(ctx, tx, v, nil); err != nil {
				return err
			}
		}
		names, marks, args := []string{"id", "body"}, []string{"?", "?"}, []any{id, string(body)}
		for _, c := range t.columns(v) {
			names, marks, args = append(names, c.name), append(marks, "?"), append(args, c.value)
		}
		insert := "INSERT INTO " + t.name + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
		if _, err := tx.ExecContext(ctx, t.s.q(insert), args...); err != nil {
			return err
		}
		if t.stored != nil {
			return t.stored(ctx, tx, v)
		}
		return nil
	})
}

func (t table[T, F]) Update(ctx context.Context, v T) error {
	body, err := storage.Encode(v)
	if err != nil {
		return err
	}
	return t.s.write(ctx, func(tx *sql.Tx) error {
		id := t.id(v)
		old, err := t.load(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.check != nil {
			if err := t.check(ctx, tx, v, &old); err != nil {
				return err
			}
		}
		sets, args := []string{"body = ?"}, []any{string(body)}
		for _, c := range t.columns(v) {
			sets, args = append(sets, c.name+" = ?"), append(args, c.value)
		}
		// The row keeps its number, and so its place in a search.
		if _, err := tx.ExecContext(ctx, t.s.q("UPDATE "+t.name+" SET "+strings.Join(sets, ", ")+" WHERE id = ?"), append(args, id)...); err != nil {
			return err
		}
		if t.stored != nil {
			return t.stored(ctx, tx, v)
		}
		return nil
	})
}

func (t table[T, F]) Delete(ctx context.Context, id string) (T, error) {
	var out T
	err := t.s.write(ctx, func(tx *sql.Tx) error {
		v, err := t.load(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.canDelete != nil {
			if err := t.canDelete(ctx, tx, v); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, t.s.q("DELETE FROM "+t.name+" WHERE id = ?"), id); err != nil {
			return err
		}
		if t.deleted != nil {
			if err := t.deleted(ctx, tx, v); err != nil {
				return err
			}
		}
		out = v
		return nil
	})
	return out, err
}

func (t table[T, F]) List(ctx context.Context, f F) ([]T, error) {
	query := "SELECT body FROM " + t.name
	conditions, args := t.where(f)
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	rows, err := t.s.db.QueryContext(ctx, t.s.q(query+" ORDER BY seq"), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []T{}
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		v, err := storage.Decode[T](body)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// conditions collects the conditions of a filter: a field that is empty
// matches everything.
type conditions struct {
	clauses []string
	args    []any
}

func (c *conditions) equal(column, value string) *conditions {
	if value != "" {
		c.clauses, c.args = append(c.clauses, column+" = ?"), append(c.args, value)
	}
	return c
}

func (c *conditions) done() ([]string, []any) { return c.clauses, c.args }

// Programs implements storage.Store.
func (s *Store) Programs() storage.Collection[openadr.Program, storage.ProgramFilter] {
	return table[openadr.Program, storage.ProgramFilter]{
		s: s, name: "programs",
		id:      func(p openadr.Program) string { return p.ID },
		columns: func(p openadr.Program) []column { return []column{{"name", p.Name}} },
		check: func(ctx context.Context, tx *sql.Tx, p openadr.Program, _ *openadr.Program) error {
			if taken, err := s.exists(ctx, tx, "SELECT 1 FROM programs WHERE name = ? AND id <> ?", p.Name, p.ID); err != nil || !taken {
				return err
			}
			return conflict("program name %q is in use", p.Name)
		},
		canDelete: func(ctx context.Context, tx *sql.Tx, p openadr.Program) error {
			if has, err := s.exists(ctx, tx, "SELECT 1 FROM events WHERE program_id = ?", p.ID); err != nil || !has {
				return err
			}
			return conflict("program %s still has events", p.ID)
		},
		where: func(storage.ProgramFilter) ([]string, []any) { return nil, nil },
	}
}

// Events implements storage.Store.
func (s *Store) Events() storage.Collection[openadr.Event, storage.EventFilter] {
	return table[openadr.Event, storage.EventFilter]{
		s: s, name: "events",
		id:      func(e openadr.Event) string { return e.ID },
		columns: func(e openadr.Event) []column { return []column{{"program_id", e.ProgramID}} },
		check: func(ctx context.Context, tx *sql.Tx, e openadr.Event, _ *openadr.Event) error {
			return s.mustExist(ctx, tx, "programs", "program", e.ProgramID)
		},
		where: func(f storage.EventFilter) ([]string, []any) {
			return new(conditions).equal("program_id", f.ProgramID).done()
		},
	}
}

// mustExist is the check of a reference.
func (s *Store) mustExist(ctx context.Context, tx *sql.Tx, table, what, id string) error {
	if is, err := s.exists(ctx, tx, "SELECT 1 FROM "+table+" WHERE id = ?", id); err != nil || is {
		return err
	}
	return conflict("%s %s does not exist", what, id)
}

// Reports implements storage.Store.
func (s *Store) Reports() storage.Collection[openadr.Report, storage.ReportFilter] {
	return table[openadr.Report, storage.ReportFilter]{
		s: s, name: "reports",
		id: func(r openadr.Report) string { return r.ID },
		columns: func(r openadr.Report) []column {
			return []column{{"event_id", r.EventID}, {"client_id", r.ClientID}, {"client_name", r.ClientName}}
		},
		check: func(ctx context.Context, tx *sql.Tx, r openadr.Report, old *openadr.Report) error {
			// A report may be updated after its event has been deleted.
			if old != nil && old.EventID == r.EventID {
				return nil
			}
			return s.mustExist(ctx, tx, "events", "event", r.EventID)
		},
		where: func(f storage.ReportFilter) ([]string, []any) {
			c := new(conditions).equal("event_id", f.EventID).equal("client_name", f.ClientName).equal("client_id", f.ClientID)
			if f.ProgramID != "" {
				// A report whose event is gone belongs to no program.
				c.clauses = append(c.clauses, "event_id IN (SELECT id FROM events WHERE program_id = ?)")
				c.args = append(c.args, f.ProgramID)
			}
			return c.done()
		},
	}
}

// VENs implements storage.Store.
func (s *Store) VENs() storage.Collection[openadr.VEN, storage.VENFilter] {
	return table[openadr.VEN, storage.VENFilter]{
		s: s, name: "vens",
		id:      func(v openadr.VEN) string { return v.ID },
		columns: func(v openadr.VEN) []column { return []column{{"name", v.Name}, {"client_id", v.ClientID}} },
		check: func(ctx context.Context, tx *sql.Tx, v openadr.VEN, _ *openadr.VEN) error {
			if taken, err := s.exists(ctx, tx, "SELECT 1 FROM vens WHERE name = ? AND id <> ?", v.Name, v.ID); err != nil || !taken {
				return err
			}
			return conflict("ven name %q is in use", v.Name)
		},
		canDelete: func(ctx context.Context, tx *sql.Tx, v openadr.VEN) error {
			if has, err := s.exists(ctx, tx, "SELECT 1 FROM resources WHERE ven_id = ?", v.ID); err != nil || !has {
				return err
			}
			return conflict("ven %s still has resources", v.ID)
		},
		where: func(f storage.VENFilter) ([]string, []any) {
			return new(conditions).equal("name", f.Name).equal("client_id", f.ClientID).done()
		},
	}
}

// Resources implements storage.Store.
func (s *Store) Resources() storage.Collection[openadr.Resource, storage.ResourceFilter] {
	return table[openadr.Resource, storage.ResourceFilter]{
		s: s, name: "resources",
		id: func(r openadr.Resource) string { return r.ID },
		columns: func(r openadr.Resource) []column {
			return []column{{"ven_id", r.VENID}, {"name", r.Name}, {"client_id", r.ClientID}}
		},
		check: func(ctx context.Context, tx *sql.Tx, r openadr.Resource, _ *openadr.Resource) error {
			if err := s.mustExist(ctx, tx, "vens", "ven", r.VENID); err != nil {
				return err
			}
			if taken, err := s.exists(ctx, tx, "SELECT 1 FROM resources WHERE ven_id = ? AND name = ? AND id <> ?", r.VENID, r.Name, r.ID); err != nil || !taken {
				return err
			}
			return conflict("resource name %q is in use for ven %s", r.Name, r.VENID)
		},
		deleted: func(ctx context.Context, tx *sql.Tx, r openadr.Resource) error {
			return s.dropChild(ctx, tx, openadr.ResourceGroupChild{Type: openadr.ChildResource, ID: r.ID})
		},
		where: func(f storage.ResourceFilter) ([]string, []any) {
			return new(conditions).equal("name", f.Name).equal("ven_id", f.VENID).equal("client_id", f.ClientID).done()
		},
	}
}

// Subscriptions implements storage.Store.
func (s *Store) Subscriptions() storage.Collection[storage.SubscriptionRecord, storage.SubscriptionFilter] {
	return table[storage.SubscriptionRecord, storage.SubscriptionFilter]{
		s: s, name: "subscriptions",
		id: func(r storage.SubscriptionRecord) string { return r.ID },
		columns: func(r storage.SubscriptionRecord) []column {
			return []column{{"program_id", r.ProgramID}, {"client_id", r.ClientID}, {"client_name", r.ClientName}}
		},
		check: func(ctx context.Context, tx *sql.Tx, r storage.SubscriptionRecord, _ *storage.SubscriptionRecord) error {
			if r.ProgramID == "" {
				return nil
			}
			return s.mustExist(ctx, tx, "programs", "program", r.ProgramID)
		},
		where: func(f storage.SubscriptionFilter) ([]string, []any) {
			return new(conditions).equal("program_id", f.ProgramID).equal("client_name", f.ClientName).equal("client_id", f.ClientID).done()
		},
	}
}

// ResourceGroups implements storage.Store.
func (s *Store) ResourceGroups() storage.Collection[openadr.ResourceGroup, storage.ResourceGroupFilter] {
	return table[openadr.ResourceGroup, storage.ResourceGroupFilter]{
		s: s, name: "resource_groups",
		id:      func(g openadr.ResourceGroup) string { return g.ID },
		columns: func(g openadr.ResourceGroup) []column { return []column{{"name", g.Name}} },
		check: func(ctx context.Context, tx *sql.Tx, g openadr.ResourceGroup, _ *openadr.ResourceGroup) error {
			for _, c := range g.Children {
				switch c.Type {
				case openadr.ChildResource:
					if err := s.mustExist(ctx, tx, "resources", "resource", c.ID); err != nil {
						return err
					}
				case openadr.ChildResourceGroup:
					// A group may hold itself.
					if c.ID == g.ID {
						continue
					}
					if err := s.mustExist(ctx, tx, "resource_groups", "resource group", c.ID); err != nil {
						return err
					}
				default:
					return conflict("%q is not a kind of child", c.Type)
				}
			}
			return nil
		},
		stored: func(ctx context.Context, tx *sql.Tx, g openadr.ResourceGroup) error {
			if _, err := tx.ExecContext(ctx, s.q("DELETE FROM resource_group_children WHERE group_id = ?"), g.ID); err != nil {
				return err
			}
			for _, c := range g.Children {
				if _, err := tx.ExecContext(ctx, s.q("INSERT INTO resource_group_children (group_id, child_type, child_id) VALUES (?, ?, ?)"), g.ID, string(c.Type), c.ID); err != nil {
					return err
				}
			}
			return nil
		},
		deleted: func(ctx context.Context, tx *sql.Tx, g openadr.ResourceGroup) error {
			if _, err := tx.ExecContext(ctx, s.q("DELETE FROM resource_group_children WHERE group_id = ?"), g.ID); err != nil {
				return err
			}
			return s.dropChild(ctx, tx, openadr.ResourceGroupChild{Type: openadr.ChildResourceGroup, ID: g.ID})
		},
		where: func(f storage.ResourceGroupFilter) ([]string, []any) {
			return new(conditions).equal("name", f.Name).done()
		},
	}
}

// dropChild takes an object that no longer exists out of the groups that
// held it: out of their documents, and out of the table that says who
// holds what.
func (s *Store) dropChild(ctx context.Context, tx *sql.Tx, child openadr.ResourceGroupChild) error {
	rows, err := tx.QueryContext(ctx, s.q("SELECT DISTINCT group_id FROM resource_group_children WHERE child_type = ? AND child_id = ?"), string(child.Type), child.ID)
	if err != nil {
		return err
	}
	var holders []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		holders = append(holders, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	groups := s.ResourceGroups().(table[openadr.ResourceGroup, storage.ResourceGroupFilter])
	for _, id := range holders {
		g, err := groups.load(ctx, tx, id)
		if err != nil {
			return err
		}
		g.Children = slices.DeleteFunc(g.Children, func(c openadr.ResourceGroupChild) bool { return c == child })
		body, err := storage.Encode(g)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q("UPDATE resource_groups SET body = ? WHERE id = ?"), string(body), id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.q("DELETE FROM resource_group_children WHERE child_type = ? AND child_id = ?"), string(child.Type), child.ID)
	return err
}

// AddDelivery implements storage.Outbox.
func (s *Store) AddDelivery(ctx context.Context, d storage.Delivery) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if taken, err := s.exists(ctx, tx, "SELECT 1 FROM deliveries WHERE id = ?", d.ID); err != nil {
			return err
		} else if taken {
			return conflict("ID %s is in use", d.ID)
		}
		_, err := tx.ExecContext(ctx, s.q("INSERT INTO deliveries (id, subscription_id, callback_url, bearer_token, body, created) VALUES (?, ?, ?, ?, ?, ?)"),
			d.ID, d.SubscriptionID, d.CallbackURL, d.BearerToken, string(d.Body), d.Created.UTC().Format(time.RFC3339Nano))
		return err
	})
}

// RemoveDelivery implements storage.Outbox.
func (s *Store) RemoveDelivery(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.q("DELETE FROM deliveries WHERE id = ?"), id)
	return err
}

// Deliveries implements storage.Outbox.
func (s *Store) Deliveries(ctx context.Context) ([]storage.Delivery, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, subscription_id, callback_url, bearer_token, body, created FROM deliveries ORDER BY seq")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []storage.Delivery
	for rows.Next() {
		var (
			d       storage.Delivery
			created string
		)
		if err := rows.Scan(&d.ID, &d.SubscriptionID, &d.CallbackURL, &d.BearerToken, &d.Body, &created); err != nil {
			return nil, err
		}
		if d.Created, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
