// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	openadr "github.com/otfabric/go-openadr"
)

// fake301 is a VTN of OpenADR 3.0.1: it has /auth/token and no
// /auth/server, and resources under their VEN. It records every request.
type fake301 struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	bodies   map[string]string
}

func newFake301(t *testing.T) *fake301 {
	f := &fake301{bodies: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		line := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			line += "?" + r.URL.RawQuery
		}
		f.mu.Lock()
		f.requests = append(f.requests, line)
		f.bodies[r.Method+" "+r.URL.Path] = string(body)
		f.mu.Unlock()
		if r.URL.Path != "/auth/token" && r.Header.Get("Authorization") != "Bearer t301" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch line := r.Method + " " + r.URL.Path; line {
		case "POST /auth/token":
			_, _ = fmt.Fprint(w, `{"access_token":"t301","token_type":"Bearer","expires_in":3600}`)
		case "GET /programs":
			_, _ = fmt.Fprint(w, `[{"id":"p1","programName":"p1","targets":[{"type":"GROUP","values":["group-1"]}]}]`)
		case "GET /events/e1":
			_, _ = fmt.Fprint(w, `{"id":"e1","programID":"p1","intervals":[]}`)
		case "GET /events":
			_, _ = fmt.Fprint(w, `[]`)
		case "POST /reports":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(body)
		case "GET /vens/v1/resources":
			_, _ = fmt.Fprint(w, `[{"id":"r1","resourceName":"meter","venID":"v1"}]`)
		case "GET /vens/v1/resources/r1", "DELETE /vens/v1/resources/r1":
			_, _ = fmt.Fprint(w, `{"id":"r1","resourceName":"meter","venID":"v1"}`)
		case "POST /vens/v1/resources", "PUT /vens/v1/resources/r1":
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"title":"Not Found","status":404,"detail":"%s"}`, line)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake301) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.requests
	f.requests = nil
	return out
}

func TestVersion301(t *testing.T) {
	ctx := context.Background()
	f := newFake301(t)
	cfg := Config{BaseURL: f.URL, Version: openadr.Version301, Credentials: &Credentials{ClientID: "c", ClientSecret: "s"}}
	ven, err := NewVEN(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bl, err := NewBL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	expect := func(t *testing.T, want ...string) {
		t.Helper()
		got := f.take()
		// The first call of each client asks for a token.
		if len(got) > 0 && got[0] == "POST /auth/token" {
			got = got[1:]
		}
		if strings.Join(got, "; ") != strings.Join(want, "; ") {
			t.Errorf("requests\n got %q\nwant %q", got, want)
		}
	}

	// The token comes from /auth/token, with no GET /auth/server first,
	// and targets are read as 3.0.1 writes them.
	programs, err := ven.Programs(ctx, ProgramQuery{Targets: []string{"group-1", "group-2"}, TargetType: "GROUP"})
	if err != nil || len(programs) != 1 || programs[0].TypedTargets[0].Type != "GROUP" {
		t.Fatalf("Programs = %+v, %v", programs, err)
	}
	if got := f.take(); strings.Join(got, "; ") != "POST /auth/token; GET /programs?targetType=GROUP&targetValues=group-1&targetValues=group-2" {
		t.Errorf("requests = %q", got)
	}

	// Searches 3.0.1 cannot make are refused before anything is sent.
	for name, err := range map[string]error{
		"targets without a type": func() error { _, err := ven.Programs(ctx, ProgramQuery{Targets: []string{"g"}}); return err }(),
		"active events":          func() error { _, err := ven.Events(ctx, EventQuery{ActiveOnly: true}); return err }(),
		"resources of no VEN":    func() error { _, err := bl.Resources(ctx, ResourceQuery{}); return err }(),
		"a bad VEN ID":           func() error { _, err := bl.Resources(ctx, ResourceQuery{VENID: "a/b"}); return err }(),
	} {
		if !errors.Is(err, openadr.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	expect(t)

	// Resources are reached through their VEN.
	if rs, err := bl.Resources(ctx, ResourceQuery{VENID: "v1", Name: "meter"}); err != nil || len(rs) != 1 {
		t.Errorf("Resources = %+v, %v", rs, err)
	}
	if r, err := ven.ResourceOf(ctx, "v1", "r1"); err != nil || r.Name != "meter" {
		t.Errorf("ResourceOf = %+v, %v", r, err)
	}
	if _, err := bl.CreateResource(ctx, openadr.Resource{Metadata: openadr.Metadata{ID: "ignored"}, VENID: "v1", Name: "meter"}); err != nil {
		t.Errorf("CreateResource: %v", err)
	}
	if _, err := ven.UpdateResource(ctx, openadr.Resource{Metadata: openadr.Metadata{ID: "r1"}, VENID: "v1", Name: "meter"}); err != nil {
		t.Errorf("UpdateResource: %v", err)
	}
	if _, err := bl.DeleteResourceOf(ctx, "v1", "r1"); err != nil {
		t.Errorf("DeleteResourceOf: %v", err)
	}
	expect(t, "GET /vens/v1/resources?resourceName=meter", "GET /vens/v1/resources/r1", "POST /vens/v1/resources", "PUT /vens/v1/resources/r1", "DELETE /vens/v1/resources/r1")

	// A report gets the program of its event, which 3.0.1 requires.
	rep := openadr.Report{EventID: "e1", ClientName: "ven-1", Resources: []openadr.ReportResource{}}
	created, err := ven.CreateReport(ctx, rep)
	if err != nil || created.ProgramID != "p1" {
		t.Errorf("CreateReport = %+v, %v", created, err)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(f.bodies["POST /reports"]), &sent)
	if sent["programID"] != "p1" {
		t.Errorf("report sent = %s", f.bodies["POST /reports"])
	}
	rep.ProgramID = "p1"
	if _, err := ven.CreateReport(ctx, rep); err != nil {
		t.Errorf("CreateReport with its program: %v", err)
	}
	expect(t, "GET /events/e1", "POST /reports", "POST /reports")
	if _, err := ven.CreateReport(ctx, openadr.Report{EventID: "e2", ClientName: "ven-1"}); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("CreateReport on an event that is not there: %v", err)
	}
	if _, err := ven.CreateReport(ctx, openadr.Report{EventID: "e1"}); !errors.Is(err, openadr.ErrInvalid) {
		t.Errorf("CreateReport without a client name: %v, want ErrInvalid before the event is read", err)
	}
	expect(t, "GET /events/e2")

	// What 3.0.1 does not have is refused before anything is sent.
	for name, err := range map[string]error{
		"Resource":       func() error { _, err := bl.Resource(ctx, "r1"); return err }(),
		"DeleteResource": func() error { _, err := ven.DeleteResource(ctx, "r1"); return err }(),
		"Notifiers":      func() error { _, err := bl.Notifiers(ctx); return err }(),
		"MQTTTopics":     func() error { _, err := ven.MQTTTopics(ctx, AllPrograms()); return err }(),
		"ResourceGroups": func() error { _, err := bl.ResourceGroups(ctx, ResourceGroupQuery{}); return err }(),
		"CreateResourceGroup": func() error {
			_, err := bl.CreateResourceGroup(ctx, openadr.ResourceGroup{Name: "g"})
			return err
		}(),
		"VEN's ResourceGroup": func() error { _, err := ven.ResourceGroup(ctx, "g1"); return err }(),
	} {
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("%s: %v, want errors.ErrUnsupported", name, err)
		}
	}
	// 3.1.0 targets cannot go to a 3.0.1 VTN.
	if _, err := bl.CreateProgram(ctx, openadr.Program{Name: "p", Targets: []string{"group-1"}}); !errors.Is(err, openadr.ErrInvalid) {
		t.Errorf("CreateProgram with string targets: %v, want ErrInvalid", err)
	}
	expect(t)

	// A configured token URL wins.
	other, err := NewBL(Config{BaseURL: f.URL + "/nowhere", Version: openadr.Version301, Credentials: &Credentials{ClientID: "c", ClientSecret: "s", TokenURL: f.URL + "/auth/token"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Events(ctx, EventQuery{}); !errors.Is(err, openadr.ErrNotFound) {
		t.Errorf("Events under the wrong base: %v, want the VTN's 404 after a token", err)
	}
	expect(t, "GET /nowhere/events")
}

func TestVersionConfig(t *testing.T) {
	if _, err := NewVEN(Config{BaseURL: "http://vtn", Version: "3.0.2"}); !errors.Is(err, openadr.ErrConfiguration) {
		t.Errorf("Version 3.0.2: %v, want ErrConfiguration", err)
	}
	for _, v := range []openadr.Version{"", openadr.Version310, openadr.Version301} {
		if _, err := NewBL(Config{BaseURL: "http://vtn", Version: v}); err != nil {
			t.Errorf("Version %q: %v", v, err)
		}
	}
	// A 3.1.0 client has no target types.
	bl, _ := NewBL(Config{BaseURL: "http://vtn.invalid"})
	if _, err := bl.Programs(context.Background(), ProgramQuery{TargetType: "GROUP"}); !errors.Is(err, openadr.ErrInvalid) {
		t.Errorf("TargetType with 3.1.0: %v, want ErrInvalid", err)
	}
	if !openadr.Version301.Supported() || !openadr.Version310.Supported() || openadr.Version("3.0.0").Supported() {
		t.Error("Version.Supported")
	}
}
