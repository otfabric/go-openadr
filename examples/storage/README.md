# Bring your own storage: SQLite and PostgreSQL

The VTN of go-openadr keeps its objects behind one interface,
[`storage.Store`](../../server/storage/storage.go), and ships two
implementations of it that need nothing but the standard library: one in
memory, one on a journal file. This directory shows how to put the VTN on a
database instead, twice:

| Package | Database | Driver |
|---|---|---|
| [`sqlite`](sqlite/sqlite.go) | a SQLite file | `modernc.org/sqlite` (no C compiler needed) |
| [`postgres`](postgres/postgres.go) | a PostgreSQL server | `github.com/jackc/pgx/v5` |

Both are a few lines around [`sqlstore`](sqlstore/sqlstore.go), one
implementation of the interface on `database/sql`, which is the part to
read and to copy.

**This is a module of its own.** go-openadr has no dependencies outside the
standard library and does not get any from here: the drivers are required
by `examples/storage/go.mod` only, and `go get github.com/otfabric/go-openadr`
never sees them. For the same reason it needs a newer Go than the main
module does (the SQLite driver decides that).

## Run it

```sh
cd examples/storage

# on SQLite
go run ./vtn -sqlite vtn.db -client utility:utility-secret:bl -client site-42:site-42-secret:ven

# on PostgreSQL
docker run -d --name openadr-db -p 127.0.0.1:5432:5432 -e POSTGRES_PASSWORD=openadr -e POSTGRES_DB=openadr postgres:18-alpine
OPENADR_POSTGRES_DSN='postgres://postgres:openadr@127.0.0.1:5432/openadr?sslmode=disable' \
    go run ./vtn -postgres -client utility:utility-secret:bl -client site-42:site-42-secret:ven
```

Then publish a tariff, read it and report on it with the clients of the
main module ([examples/tariff](../tariff/main.go),
[examples/ven](../ven/main.go)), stop the VTN, start it again, and find
everything where it was.

## How the store is built

- **One JSON document per object.** `storage.Encode` turns an object into
  the JSON the API gives it, and `storage.Decode` reads it back, so the
  schema does not have to follow every property of OpenADR. The document is
  `JSONB` in PostgreSQL and text in SQLite.
- **A few columns beside it**: what the VTN searches by (a program's name,
  an event's program, the client an object belongs to), and a number that
  keeps the order of creation, which is the order of a search.
- **The rules, in the transaction of the write.** A store decides what has
  to be decided together with a write: names are unique, a reference leads
  somewhere, a program with events is not deleted, a deleted resource
  leaves its resource groups. A violation is a `*storage.ConstraintError`,
  which the VTN answers with 409 and the words the store gives it. The
  database's own constraints stand behind the checks.
- **Writers take turns.** A write begins by taking one lock for the store:
  the single connection in SQLite, an advisory lock in PostgreSQL, which
  also holds between several VTN processes on one database. That keeps the
  checks simple and right. A VTN with heavy write traffic would rely on the
  database's constraints alone and translate their errors.
- **Who may see what is not here.** Ownership and targeting are the VTN's,
  above the store, so every store behaves the same.

## Test your own

`storagetest.Run` is the suite the stores of go-openadr pass, these two
included, and it is public for yours:

```go
func TestMyStore(t *testing.T) {
    storagetest.Run(t, storagetest.Config{
        New:    func(t *testing.T) storage.Store { return openEmpty(t) },
        Reopen: func(t *testing.T, s storage.Store) storage.Store { return closeAndOpen(t, s) },
    })
}
```

The tests here also run a VTN on each store, with the clients of the
module, across a restart, and for PostgreSQL with two stores on one
database, as two VTN processes would be.

```sh
go test ./...                              # SQLite always; PostgreSQL when Docker is there
OPENADR_STORAGE_REQUIRE=1 go test ./...    # PostgreSQL must run, as in CI
OPENADR_POSTGRES_DSN=postgres://... go test ./postgres   # on a server of your own
```

## What it is not

An example. It has no migrations, no connection pool tuning, no metrics,
and it serializes writes. It keeps the bearer tokens of subscriptions in
the clear, as every store of the VTN does: protect the database.
