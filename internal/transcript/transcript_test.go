// SPDX-License-Identifier: MIT

package transcript_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/otfabric/go-openadr/internal/transcript"
)

func diffKinds(ds []transcript.Diff) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Path+" "+d.Kind)
	}
	return strings.Join(out, "; ")
}

// The comparison tells null, absent and a different type apart, and
// compares numbers by value.
func TestCompare(t *testing.T) {
	for _, c := range []struct {
		want, got string
		rules     transcript.Rules
		diffs     string
	}{
		{`{"a":1}`, `{"a":1.0}`, transcript.Rules{}, ""},
		{`{"a":null}`, `{}`, transcript.Rules{}, "/a missing"},
		{`{}`, `{"a":null}`, transcript.Rules{}, "/a extra"},
		{`{}`, `{"a":null}`, transcript.Rules{IgnoreExtra: true}, ""},
		{`{"a":null}`, `{"a":0}`, transcript.Rules{}, "/a type"},
		{`{"a":"1"}`, `{"a":1}`, transcript.Rules{}, "/a type"},
		{`{"a":[1,2]}`, `{"a":[1]}`, transcript.Rules{}, "/a length"},
		{`{"a/b":true}`, `{"a/b":false}`, transcript.Rules{}, "/a~1b value"},
		{`{"a":{"b":1}}`, `{"a":{"b":2}}`, transcript.Rules{Ignore: []string{"/a/*"}}, ""},
		{`{"start":"2030-01-01T00:00:00+00:00"}`, `{"start":"2030-01-01T00:00:00Z"}`, transcript.Rules{}, "/start value"},
		{`{"start":"2030-01-01T00:00:00+00:00"}`, `{"start":"2030-01-01T00:00:00Z"}`, transcript.Rules{Equivalent: true}, ""},
		{`{"duration":"P0Y0M0DT1H0M0S"}`, `{"duration":"PT1H"}`, transcript.Rules{Equivalent: true}, ""},
		{`{"duration":"PT1H"}`, `{"duration":"PT2H"}`, transcript.Rules{Equivalent: true}, "/duration value"},
		{`{"createdDateTime":"x"}`, `{"createdDateTime":"2030-01-01T00:00:00Z"}`, transcript.Rules{Timestamps: true}, "/createdDateTime value"},
		{`{"access_token":"a","expires_in":1}`, `{"access_token":"b","expires_in":2}`, transcript.Rules{Tokens: true}, ""},
		{`{"access_token":"a"}`, `{"access_token":""}`, transcript.Rules{Tokens: true}, "/access_token value"},
		{`{`, `{}`, transcript.Rules{}, " syntax"},
		{`{}`, `{} {}`, transcript.Rules{}, " syntax"},
	} {
		if got := diffKinds(transcript.Compare([]byte(c.want), []byte(c.got), c.rules)); got != c.diffs {
			t.Errorf("Compare(%s, %s) = %q, want %q", c.want, c.got, got, c.diffs)
		}
	}
}

// IDs map one to one, in members and inside strings.
func TestCompareIDs(t *testing.T) {
	ids := transcript.NewIDMap()
	r := transcript.Rules{IDs: ids}
	if d := transcript.Compare([]byte(`{"id":"a","programID":"b"}`), []byte(`{"id":"1","programID":"2"}`), r); len(d) != 0 {
		t.Fatal(d)
	}
	if d := transcript.Compare([]byte(`{"id":"a"}`), []byte(`{"id":"2"}`), r); diffKinds(d) != "/id id" {
		t.Errorf("a second mapping of a: %v", d)
	}
	if d := transcript.Compare([]byte(`{"topic":"vens/a/events"}`), []byte(`{"topic":"vens/1/events"}`), r); len(d) != 0 {
		t.Errorf("an ID in a string: %v", d)
	}
	if ids.Map("a") != "1" || ids.Map("z") != "z" {
		t.Error("Map")
	}
	if d := transcript.Compare([]byte(`{"clientID":"zz"}`), []byte(`{"clientID":"yy"}`), r); diffKinds(d) != "/clientID value" {
		t.Errorf("clientID is no object ID: %v", d)
	}
}

func TestRoute(t *testing.T) {
	for path, want := range map[string]string{
		"/":                                     "/",
		"/programs":                             "/programs",
		"/programs/p1":                          "/programs/{programID}",
		"/vens/v1/resources/r1":                 "/vens/{venID}/resources/{resourceID}",
		"/resource_groups/g":                    "/resource_groups/{resourceGroupID}",
		"/notifiers/mqtt/topics/programs":       "/notifiers/mqtt/topics/programs",
		"/notifiers/mqtt/topics/vens/v1/events": "/notifiers/mqtt/topics/vens/{venID}/events",
		"/auth/token":                           "/auth/token",
	} {
		if got := transcript.Route(path); got != want {
			t.Errorf("Route(%s) = %s, want %s", path, got, want)
		}
	}
	if transcript.Kind("/vens/{venID}/resources") != "vens/resources" || transcript.Kind("/auth/token") != "" {
		t.Error("Kind")
	}
}

// Redaction is deterministic and keeps the meaning that two values were
// the same.
func TestRedactor(t *testing.T) {
	r := transcript.NewRedactor()
	r.Secret("bl", "s3cret")
	r.Endpoint("http://127.0.0.1:5000", "http://vtn.invalid")
	got := r.Text(`{"access_token":"tok1","tokenURL":"http://127.0.0.1:5000/auth/token","broker":"mqtt://127.0.0.1:1883","x":"s3cret"}`)
	want := `{"access_token":"redacted-token-1","tokenURL":"http://vtn.invalid/auth/token","broker":"mqtt://broker.invalid","x":"redacted-secret-bl"}`
	if got != want {
		t.Errorf("Text:\n%s\n%s", got, want)
	}
	if a := r.Authorization("Bearer tok1"); a != "Bearer redacted-token-1" {
		t.Errorf("Authorization = %s", a)
	}
	basic := r.Authorization("Basic " + base64.StdEncoding.EncodeToString([]byte("bl-id:s3cret")))
	if raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(basic, "Basic ")); string(raw) != "bl-id:redacted-secret-bl" {
		t.Errorf("Basic = %s", raw)
	}
	if f := r.Form(map[string][]string{"client_secret": {"other"}}); f["client_secret"][0] != "redacted-secret-1" {
		t.Errorf("Form = %v", f)
	}
	if len(r.Summary()) == 0 {
		t.Error("no summary")
	}
}

// The recorder records both directions, redacts, and tracks actors and
// object state.
func TestRecorder(t *testing.T) {
	red := transcript.NewRedactor()
	rec := transcript.NewRecorder(transcript.File{Name: "x", Source: "s", Level: "D"},
		[]transcript.Client{{Name: "bl", Role: "bl", ClientID: "bl-id"}}, map[string]string{"bl": "pw"}, red, transcript.OriginPeer)
	vtn := http.NewServeMux()
	vtn.HandleFunc("POST /auth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"T","token_type":"Bearer"}`)
	})
	vtn.HandleFunc("POST /programs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"p1","programName":"x"}`)
	})
	vtn.HandleFunc("DELETE /programs/p1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"p1"}`)
	})
	srv := httptest.NewServer(rec.Handler(vtn))
	defer srv.Close()
	hc := srv.Client()
	if _, err := hc.PostForm(srv.URL+"/auth/token", url.Values{"grant_type": {"client_credentials"}, "client_id": {"bl-id"}, "client_secret": {"pw"}}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/programs", strings.NewReader(`{"programName":"x"}`))
	req.Header.Set("Authorization", "Bearer T")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(transcript.OriginHeader, transcript.OriginSelf)
	req.Header.Set(transcript.NoteHeader, "create")
	if _, err := hc.Do(req); err != nil {
		t.Fatal(err)
	}
	// The same through Transport, as a client of a peer's VTN.
	tc := &http.Client{Transport: rec.Transport(http.DefaultTransport)}
	del, _ := http.NewRequest(http.MethodDelete, srv.URL+"/programs/p1", nil)
	del.Header.Set("Authorization", "Bearer T")
	if _, err := tc.Do(del); err != nil {
		t.Fatal(err)
	}
	rec.Message("events/create", []byte(`{"objectType":"EVENT"}`))

	f := rec.File()
	if len(f.Steps) != 5 || rec.Len() != 5 {
		t.Fatalf("%d steps", len(f.Steps))
	}
	tok, create := f.Steps[0], f.Steps[1]
	if tok.Actor != transcript.ActorBL || tok.Request.Form["client_secret"][0] != "redacted-secret-bl" || !strings.Contains(string(tok.Response.JSON), "redacted-token-1") {
		t.Errorf("token step: %+v %s", tok.Request.Form, tok.Response.JSON)
	}
	if create.Actor != transcript.ActorBL || create.Origin != transcript.OriginSelf || create.Note != "create" || create.Request.Headers["Authorization"] != "Bearer redacted-token-1" {
		t.Errorf("create step: %+v", create)
	}
	if create.State["programs"][0] != "p1" || f.Steps[2].Origin != transcript.OriginPeer {
		t.Errorf("state %v", create.State)
	}
	if f.Steps[4].Transport != "mqtt" {
		t.Error("message")
	}
	data, err := transcript.Marshal(f)
	if err != nil || strings.Contains(string(data), `"pw"`) || strings.Contains(string(data), `"T"`) {
		t.Errorf("marshal: %v", err)
	}
}
