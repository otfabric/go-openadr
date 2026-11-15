// SPDX-License-Identifier: MIT

package server_test

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// heldBody is a request body that, when it is first read, says so on
// reading and then waits for release: a client that is slow to send it.
type heldBody struct {
	once    sync.Once
	reading chan struct{}
	release chan struct{}
	body    io.Reader
}

func newHeldBody(body string) *heldBody {
	return &heldBody{reading: make(chan struct{}), release: make(chan struct{}), body: strings.NewReader(body)}
}

func (b *heldBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.reading)
		<-b.release
	})
	return b.body.Read(p)
}

// start serves req on its own goroutine and returns the answer when done
// is read.
func start(h http.Handler, req *http.Request) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		done <- rec
	}()
	return done
}

func putAs(token, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest("PUT", target, body)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func created(t *testing.T, h http.Handler, path, body string) string {
	t.Helper()
	r := call(h, "POST", path, "bl", body)
	if r.status != http.StatusCreated {
		t.Fatalf("POST %s = %d %s", path, r.status, r.body)
	}
	return r.json["id"].(string)
}

// A VEN that is slow to send the body of an update of its own ven or
// resource cannot write back the targets, or the client, that Business
// Logic has changed in the meantime: the update applies to the object as
// it is stored when the body has arrived.
func TestSlowUpdateKeepsWhatBusinessLogicChanged(t *testing.T) {
	type change struct {
		name, path, venBody, blBody string
		wantStatus                  int
		check                       func(map[string]any) bool
	}
	noTargets := func(o map[string]any) bool { return o["targets"] == nil || len(o["targets"].([]any)) == 0 }
	for _, c := range []change{
		{"ven targets revoked", "/vens/", `{"objectType":"VEN_VEN_REQUEST","venName":"v"}`,
			`{"objectType":"BL_VEN_REQUEST","venName":"v","clientID":"ven","targets":[]}`, 200, noTargets},
		{"ven given to another client", "/vens/", `{"objectType":"VEN_VEN_REQUEST","venName":"v"}`,
			`{"objectType":"BL_VEN_REQUEST","venName":"v","clientID":"other","targets":["t1"]}`, 404,
			func(o map[string]any) bool { return o["clientID"] == "other" }},
		{"resource targets revoked", "/resources/", `{"objectType":"VEN_RESOURCE_REQUEST","resourceName":"r","venID":"%s"}`,
			`{"objectType":"BL_RESOURCE_REQUEST","resourceName":"r","venID":"%s","clientID":"ven","targets":[]}`, 200, noTargets},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newServer(t, server.Config{})
			venID := created(t, h, "/vens", `{"objectType":"BL_VEN_REQUEST","venName":"v","clientID":"ven","targets":["t1"]}`)
			id := venID
			if c.path == "/resources/" {
				id = created(t, h, "/resources", `{"objectType":"BL_RESOURCE_REQUEST","resourceName":"r","venID":"`+venID+`","clientID":"ven","targets":["t1"]}`)
			}
			venBody, blBody := c.venBody, c.blBody
			if strings.Contains(venBody, "%s") {
				venBody, blBody = fmt.Sprintf(venBody, venID), fmt.Sprintf(blBody, venID)
			}

			body := newHeldBody(venBody)
			done := start(h, putAs("ven", c.path+id, body))
			<-body.reading
			if r := call(h, "PUT", c.path+id, "bl", blBody); r.status != 200 {
				t.Fatalf("Business Logic's update = %d %s", r.status, r.body)
			}
			close(body.release)
			if rec := <-done; rec.Code != c.wantStatus {
				t.Errorf("the VEN's update = %d %s, want %d", rec.Code, rec.Body, c.wantStatus)
			}
			got := call(h, "GET", c.path+id, "bl", "")
			if got.status != 200 || !c.check(got.json) {
				t.Errorf("stored after both updates: %d %s", got.status, got.body)
			}
		})
	}
}

// gatedVENs holds up the first Get of a ven once it is armed, until
// release: a store that is slow to answer.
type gatedVENs struct {
	storage.Collection[openadr.VEN, storage.VENFilter]
	armed   atomic.Bool
	getting chan struct{}
	release chan struct{}
}

func (c *gatedVENs) Get(ctx context.Context, id string) (openadr.VEN, error) {
	if c.armed.CompareAndSwap(true, false) {
		close(c.getting)
		<-c.release
	}
	return c.Collection.Get(ctx, id)
}

type gatedStore struct {
	storage.Store
	vens *gatedVENs
}

func (s gatedStore) VENs() storage.Collection[openadr.VEN, storage.VENFilter] { return s.vens }

// A delete that has found the ven of the VEN comes before an update of
// Business Logic that arrives while the store is answering: it does not
// delete a ven that Business Logic has given to another client meanwhile.
func TestDeleteIsNotOvertaken(t *testing.T) {
	mem := storage.NewMemory()
	vens := &gatedVENs{Collection: mem.VENs(), getting: make(chan struct{}), release: make(chan struct{})}
	h := newServer(t, server.Config{Store: gatedStore{mem, vens}})
	id := created(t, h, "/vens", `{"objectType":"BL_VEN_REQUEST","venName":"v","clientID":"ven"}`)

	vens.armed.Store(true)
	del := httptest.NewRequest("DELETE", "/vens/"+id, nil)
	del.Header.Set("Authorization", "Bearer ven")
	deleted := start(h, del)
	<-vens.getting
	moved := start(h, putAs("bl", "/vens/"+id, strings.NewReader(`{"objectType":"BL_VEN_REQUEST","venName":"v","clientID":"other"}`)))
	close(vens.release)
	if rec := <-deleted; rec.Code != 200 {
		t.Errorf("DELETE = %d %s", rec.Code, rec.Body)
	}
	if rec := <-moved; rec.Code != 404 {
		t.Errorf("Business Logic's update after the delete = %d %s, want 404", rec.Code, rec.Body)
	}
}

// countingResources counts the searches of resources.
type countingResources struct {
	storage.Collection[openadr.Resource, storage.ResourceFilter]
	lists atomic.Int64
}

func (c *countingResources) List(ctx context.Context, f storage.ResourceFilter) ([]openadr.Resource, error) {
	c.lists.Add(1)
	return c.Collection.List(ctx, f)
}

type countingStore struct {
	storage.Store
	resources *countingResources
}

func (s countingStore) Resources() storage.Collection[openadr.Resource, storage.ResourceFilter] {
	return s.resources
}

// The targets a VEN was granted through the resources of its vens are
// found with one search of resources, however many vens it has.
func TestGrantedTargetsSearchResourcesOnce(t *testing.T) {
	mem := storage.NewMemory()
	resources := &countingResources{Collection: mem.Resources()}
	h := newServer(t, server.Config{Store: countingStore{mem, resources}})

	const vens = 20
	for i := range vens {
		venID := created(t, h, "/vens", fmt.Sprintf(`{"objectType":"BL_VEN_REQUEST","venName":"v%d","clientID":"ven"}`, i))
		created(t, h, "/resources", fmt.Sprintf(`{"objectType":"BL_RESOURCE_REQUEST","resourceName":"r","venID":"%s","clientID":"ven","targets":["t%d"]}`, venID, i))
	}
	// A ven of another client, whose resource carries the client ID of
	// the VEN: it grants the VEN nothing.
	otherVEN := created(t, h, "/vens", `{"objectType":"BL_VEN_REQUEST","venName":"o","clientID":"other"}`)
	created(t, h, "/resources", `{"objectType":"BL_RESOURCE_REQUEST","resourceName":"r","venID":"`+otherVEN+`","clientID":"ven","targets":["secret"]}`)

	granted := created(t, h, "/programs", `{"programName":"p","targets":["t13"]}`)
	hidden := created(t, h, "/programs", `{"programName":"q","targets":["secret"]}`)

	resources.lists.Store(0)
	if r := call(h, "GET", "/programs/"+granted, "ven", ""); r.status != 200 {
		t.Errorf("a program targeted at a resource of the VEN: %d %s", r.status, r.body)
	}
	if r := call(h, "GET", "/programs/"+hidden, "ven", ""); r.status != 404 {
		t.Errorf("a program targeted at a resource of another client's ven: %d %s", r.status, r.body)
	}
	if n := resources.lists.Load(); n != 2 {
		t.Errorf("%d searches of resources for two reads, want 2", n)
	}

	// With a single ven, the search names it.
	single := created(t, h, "/vens", `{"objectType":"BL_VEN_REQUEST","venName":"s","clientID":"one"}`)
	created(t, h, "/resources", `{"objectType":"BL_RESOURCE_REQUEST","resourceName":"r","venID":"`+single+`","clientID":"one","targets":["t13"]}`)
	one := auth.ValidatorFunc(func(context.Context, string) (auth.Identity, error) {
		return auth.Identity{ClientID: "one", Scopes: auth.VENScopes()}, nil
	})
	vtn, err := server.New(server.Config{Store: countingStore{mem, resources}, Validator: one, TokenURL: "https://idp.example/token"})
	if err != nil {
		t.Fatal(err)
	}
	if r := call(vtn, "GET", "/programs/"+granted, "x", ""); r.status != 200 {
		t.Errorf("a program targeted at the resource of a single ven: %d %s", r.status, r.body)
	}
	// A client without vens is granted nothing and searches nothing.
	none := auth.ValidatorFunc(func(context.Context, string) (auth.Identity, error) {
		return auth.Identity{ClientID: "none", Scopes: auth.VENScopes()}, nil
	})
	vtn, err = server.New(server.Config{Store: countingStore{mem, resources}, Validator: none, TokenURL: "https://idp.example/token"})
	if err != nil {
		t.Fatal(err)
	}
	resources.lists.Store(0)
	if r := call(vtn, "GET", "/programs/"+granted, "x", ""); r.status != 404 || resources.lists.Load() != 0 {
		t.Errorf("a client without vens: %d, %d searches", r.status, resources.lists.Load())
	}
}

// Answers about subscriptions carry their bearer tokens and are never
// compressed, whatever their size.
func TestSubscriptionAnswersAreNotCompressed(t *testing.T) {
	h := newServer(t, server.Config{Webhooks: fakeWebhooks{}})
	sub := `{"clientName":"c","objectOperations":[{"objects":["PROGRAM"],"operations":["CREATE"],` +
		`"callbackUrl":"https://hook.example/` + strings.Repeat("a", 1500) + `","bearerToken":"secret"}]}`
	id := created(t, h, "/subscriptions", sub)
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/subscriptions", nil),
		httptest.NewRequest("GET", "/subscriptions/"+id, nil),
		httptest.NewRequest("POST", "/subscriptions", strings.NewReader(sub)),
		httptest.NewRequest("PUT", "/subscriptions/"+id, strings.NewReader(sub)),
	} {
		req.Header.Set("Authorization", "Bearer bl")
		req.Header.Set("Accept-Encoding", "gzip, deflate")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code/100 != 2 || rec.Body.Len() < 1024 || rec.Header().Get("Content-Encoding") != "" || !strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s %s: %d, %d bytes, Content-Encoding %q", req.Method, req.URL.Path, rec.Code, rec.Body.Len(), rec.Header().Get("Content-Encoding"))
		}
	}
	// Other answers of that size still are.
	for i := range 20 {
		created(t, h, "/programs", fmt.Sprintf(`{"programName":"%d%s"}`, i, strings.Repeat("p", 100)))
	}
	req := httptest.NewRequest("GET", "/programs", nil)
	req.Header.Set("Authorization", "Bearer bl")
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("GET /programs of %d bytes was not compressed", rec.Body.Len())
	}
}

// Only a request without an Authorization header is anonymous: an empty
// one is a token that is missing.
func TestEmptyAuthorizationIsNotAnonymous(t *testing.T) {
	h := newServer(t, server.Config{AllowAnonymous: true})
	req := httptest.NewRequest("GET", "/programs", nil)
	req.Header["Authorization"] = []string{""}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("empty Authorization header: %d %s", rec.Code, rec.Body)
	}
	if r := call(h, "GET", "/programs", "", ""); r.status != 200 {
		t.Errorf("no Authorization header: %d %s", r.status, r.body)
	}
}

// A Validator that accepts a token and names no client for it is a fault
// of the deployment, not a client that may see every client's objects.
func TestIdentityWithoutClientID(t *testing.T) {
	for _, scopes := range [][]string{auth.VENScopes(), auth.BusinessLogicScopes()} {
		v := auth.ValidatorFunc(func(context.Context, string) (auth.Identity, error) {
			return auth.Identity{Scopes: scopes}, nil
		})
		vtn, err := server.New(server.Config{Store: storage.NewMemory(), Validator: v, TokenURL: "https://idp.example/token"})
		if err != nil {
			t.Fatal(err)
		}
		if r := call(vtn, "GET", "/vens", "x", ""); r.status != http.StatusInternalServerError {
			t.Errorf("scopes %v: %d %s", scopes, r.status, r.body)
		}
	}
}

// A rate that is not a number takes no request, as one of 0 does; an
// infinite rate takes every request.
func TestRateLimiterOfNoNumber(t *testing.T) {
	if ok, wait := server.NewRateLimiter(math.NaN(), 5).Allow("a", "GET /programs"); ok || wait != 0 {
		t.Errorf("NaN: %v, %v", ok, wait)
	}
	l := server.NewRateLimiter(math.Inf(1), 1)
	for i := range 100 {
		if ok, _ := l.Allow("a", "GET /programs"); !ok {
			t.Fatalf("+Inf refused request %d", i+1)
		}
	}
	if ok, _ := server.NewRateLimiter(math.Inf(-1), 1).Allow("a", "GET /programs"); ok {
		t.Error("-Inf took a request")
	}
}
