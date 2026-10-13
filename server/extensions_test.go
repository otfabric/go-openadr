// SPDX-License-Identifier: MIT

package server_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// A VTN that authenticates nobody: no validator, no token endpoint.
func TestNonAuthenticatingVTN(t *testing.T) {
	store := storage.NewMemory()
	if _, err := server.New(server.Config{Store: store}); err == nil {
		t.Error("a VTN without a Validator that does not let anyone in")
	}
	if _, err := server.New(server.Config{Store: store, AllowAnonymous: true, TokenURL: "auth/token"}); err == nil {
		t.Error("a relative TokenURL")
	}
	vtn, err := server.New(server.Config{Store: store, AllowAnonymous: true, ResourceGroups: true, WebSocket: &server.WebSocketConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vtn.Close() }()

	if r := call(vtn, "GET", "/programs", "", ""); r.status != 200 || r.body != "[]" {
		t.Errorf("programs: %d %s", r.status, r.body)
	}
	if r := call(vtn, "GET", "/events?active=true", "", ""); r.status != 200 {
		t.Errorf("events: %d %s", r.status, r.body)
	}
	// Nobody can have a token, so a token is not accepted.
	if r := call(vtn, "GET", "/programs", "anything", ""); r.status != 401 || !strings.Contains(r.header.Get("WWW-Authenticate"), "invalid_token") {
		t.Errorf("with a token: %d %v", r.status, r.header)
	}
	// And it says so to a client that looks for the token endpoint.
	if r := call(vtn, "GET", "/auth/server", "", ""); r.status != 501 || !strings.Contains(r.body, "does not authenticate") {
		t.Errorf("auth/server: %d %s", r.status, r.body)
	}
	if r := call(vtn, "POST", "/auth/token", "", ""); r.status != 501 {
		t.Errorf("auth/token: %d %s", r.status, r.body)
	}
	for _, path := range []string{"/reports", "/vens", "/resources", "/subscriptions", "/resource_groups", "/notifiers/ws"} {
		if r := call(vtn, "GET", path, "", ""); r.status != 401 || r.header.Get("WWW-Authenticate") != "Bearer" || r.json["status"] != float64(401) {
			t.Errorf("%s: %d %s", path, r.status, r.body)
		}
	}
	if r := call(vtn, "POST", "/programs", "", `{"programName":"x"}`); r.status != 401 {
		t.Errorf("a write: %d %s", r.status, r.body)
	}
	if r := call(vtn, "GET", "/notifiers", "", ""); r.status != 200 || r.json["WEBSOCKET"] != true || r.json["WEBHOOK"] != false {
		t.Errorf("notifiers: %d %s", r.status, r.body)
	}
}

func TestCompressionNegotiation(t *testing.T) {
	store := storage.NewMemory()
	vtn := newServer(t, server.Config{Store: store})
	for i := range 30 {
		body := `{"programName":"program-` + string(rune('a'+i)) + `","programDescriptions":[{"URL":"https://example.com/a-long-description"}]}`
		if r := call(vtn, "POST", "/programs", "bl", body); r.status != 201 {
			t.Fatalf("%d %s", r.status, r.body)
		}
	}
	get := func(h http.Handler, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/programs", nil)
		req.Header.Set("Authorization", "Bearer bl")
		if accept != "-" {
			req.Header.Set("Accept-Encoding", accept)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	plain := get(vtn, "-").Body.Bytes()
	for accept, want := range map[string]string{
		"-": "", "": "", "identity": "", "br, zstd": "", "compress;q=1": "",
		"gzip": "gzip", "GZIP": "gzip", " gzip ; q=0.5": "gzip", "br;q=1.0, gzip;q=0.1": "gzip",
		"deflate": "deflate", "Deflate, br": "deflate", "zstd, deflate;q=0.2": "deflate",
		// Both: the heavier, and gzip when they weigh the same.
		"deflate, gzip": "gzip", "gzip, deflate, br, zstd": "gzip", "gzip;q=0.5, deflate": "deflate", "deflate;q=0.9, gzip;q=0.8": "deflate",
		// The star is every coding that is not named.
		"*": "gzip", "*;q=0.5, deflate": "deflate", "gzip;q=0, *": "deflate", "*;q=0": "",
		// Refused, or not a weight.
		"gzip;q=0": "", "gzip; q=0.0": "", "gzip;q=0.000, deflate;q=0": "", "gzip;q=0, deflate": "deflate",
		"gzip;q=2": "", "gzip;q=x, deflate": "deflate",
	} {
		rec := get(vtn, accept)
		got := rec.Header().Get("Content-Encoding")
		if got != want {
			t.Errorf("Accept-Encoding %q: answered with %q, want %q", accept, got, want)
			continue
		}
		body := rec.Body.Bytes()
		var zr io.Reader
		var err error
		switch got {
		case "gzip":
			zr, err = gzip.NewReader(bytes.NewReader(body))
		case "deflate":
			zr, err = zlib.NewReader(bytes.NewReader(body))
		}
		if err != nil {
			t.Fatalf("Accept-Encoding %q: %v", accept, err)
		}
		if zr != nil {
			if len(body) >= len(plain) {
				t.Errorf("Accept-Encoding %q: %d bytes became %d", accept, len(plain), len(body))
			}
			body, _ = io.ReadAll(zr)
		}
		if !bytes.Equal(body, plain) {
			t.Errorf("Accept-Encoding %q: another body", accept)
		}
		if rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Errorf("Accept-Encoding %q: Vary %q", accept, rec.Header().Get("Vary"))
		}
	}
	off, err := server.New(server.Config{Store: store, Validator: tokens, TokenURL: "https://idp.example/token", DisableCompression: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(off, "gzip"); rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), plain) {
		t.Error("compression was not turned off")
	}
}

// Without the switch, the paths of the extensions do not exist.
func TestExtensionsAreOptIn(t *testing.T) {
	vtn := newServer(t, server.Config{Webhooks: fakeWebhooks{}})
	if r := call(vtn, "GET", "/resource_groups", "bl", ""); r.status != 404 {
		t.Errorf("resource groups: %d %s", r.status, r.body)
	}
	if r := call(vtn, "GET", "/notifiers/ws", "bl", ""); r.status != 501 {
		t.Errorf("the WebSocket: %d %s", r.status, r.body)
	}
	if r := call(vtn, "GET", "/notifiers", "bl", ""); r.status != 200 || strings.Contains(r.body, "WEBSOCKET") {
		t.Errorf("notifiers of a 3.1.0 VTN name an extension: %s", r.body)
	}
	// The store of such a VTN is still asked nothing about groups.
	plain, _ := server.New(server.Config{Store: storage.NewMemory(), Validator: tokens, TokenURL: "https://idp.example/token"})
	if err := plain.Close(); err != nil {
		t.Errorf("closing a VTN that has nothing open: %v", err)
	}
}
