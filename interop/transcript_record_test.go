//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/internal/mqtt"
	"github.com/otfabric/go-openadr/internal/transcript"
)

// Recording transcripts (testdata/transcripts/README.md).
//
// With OPENADR_TRANSCRIPT_RECORD set to a directory, every test of this
// suite records what passes between this module and the reference
// implementation into <dir>/<source>/<name>.json:
//
//   - a reference VTN is reached through a recording reverse proxy, which
//     the VTN is also told is its public URL, so that what it announces
//     (/auth/server) leads there too;
//   - this module's VTN, used by a reference client, records through a
//     handler around it; the test's own requests to it say so in a header;
//   - a webhook receiver of this module records what a reference VTN
//     sends to it, and an MQTT subscriber what a reference VTN publishes.
//
// Credentials and the ports of the test network are replaced as the
// transcript package documents. Nothing is recorded without the variable,
// and a failed test writes nothing.

var (
	recMu     sync.Mutex
	recorders = map[string]*testRecording{}
)

type testRecording struct {
	rec *transcript.Recorder
	red *transcript.Redactor
	seq int
}

// implementations names the reference implementations of the pinned images.
var implementations = map[string]struct{ peer, version, protocol, role string }{
	"openleadr":    {"openleadr", "0.2.8", "3.1.0", "VTN"},
	"openleadr301": {"openleadr", "0.1.4", "3.0.1", "VTN"},
	"cbsigma":      {"cbsigma", "0.2.0", "3.1.0", "VTN"},
	"elaadnl":      {"elaadnl", "2.4.2", "3.1.0", "VEN+BL"},
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// operation turns a test name into the operation part of a transcript name.
func operation(t *testing.T, adapter string) string {
	name := strings.TrimPrefix(t.Name(), "Test")
	for _, p := range []string{"TranscriptScenario", "OpenLEADR301", "OpenLEADR", "Cbsigma", "ElaadNL", "Otfabric", "Openleadr"} {
		name = strings.ReplaceAll(name, p, "")
	}
	if name == "" {
		name = "Scenario"
	}
	name = strings.ReplaceAll(name, "VTN", "Vtn")
	name = strings.ReplaceAll(name, "VENs", "Vens")
	name = strings.ReplaceAll(name, "MQTT", "Mqtt")
	name = strings.ReplaceAll(name, "TLS", "Tls")
	name = strings.ReplaceAll(name, "ByID", "ById")
	// CamelCase to words.
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	op := strings.Trim(nonWord.ReplaceAllString(strings.ToLower(b.String()), "-"), "-")
	if op == "" {
		op = "session"
	}
	return op
}

// recording returns the recorder of the running test, starting it on first
// use; nil without OPENADR_TRANSCRIPT_RECORD. peerIsVTN says whether the
// reference implementation is the VTN (it answers) or the client. fresh
// starts a transcript of its own for a test that starts a second VTN: its
// name has the number of the VTN appended.
func recording(t *testing.T, adapter, fixtureName string, peerIsVTN, fresh bool) *testRecording {
	t.Helper()
	dir := os.Getenv("OPENADR_TRANSCRIPT_RECORD")
	if dir == "" {
		return nil
	}
	recMu.Lock()
	defer recMu.Unlock()
	seq := 1
	if r, ok := recorders[t.Name()]; ok {
		if !fresh {
			return r
		}
		seq = r.seq + 1
	}
	impl, ok := implementations[adapter]
	if !ok {
		return nil
	}
	fx := readFixture(t, image(adapter), fixtureName)
	red := transcript.NewRedactor()
	var clients []transcript.Client
	secrets := map[string]string{}
	for _, c := range fx.Clients {
		clients = append(clients, transcript.Client{Name: c.Name, Role: c.Role, ClientID: c.ClientID})
		secrets[c.Name] = c.ClientSecret
	}
	device := fmt.Sprintf("%s-%s", impl.peer, impl.version)
	name := "go-openadr_" + device + "_" + operation(t, adapter)
	defOrigin := transcript.OriginSelf
	role := impl.role
	if !peerIsVTN {
		name = device + "_go-openadr_" + operation(t, adapter)
		defOrigin = transcript.OriginPeer
		if role == "VTN" {
			role = "VEN+BL"
		}
	}
	if seq > 1 {
		name += fmt.Sprintf("-%d", seq)
	}
	meta := transcript.File{
		Name: name, Source: adapter, Level: "D", ProtocolVersion: impl.protocol,
		Peer: fmt.Sprintf("%s %s (%s)", impl.peer, impl.version, image(adapter)), PeerRole: role,
		Recorded: time.Now().UTC().Format("2006-01-02"),
		Recorder: "go-openadr interop suite, " + t.Name(), Fixture: fixtureName,
	}
	r := &testRecording{red: red, rec: transcript.NewRecorder(meta, clients, secrets, red, defOrigin), seq: seq}
	recorders[t.Name()] = r
	t.Cleanup(func() {
		recMu.Lock()
		if recorders[t.Name()] == r {
			delete(recorders, t.Name())
		}
		recMu.Unlock()
		if t.Failed() || t.Skipped() || r.rec.Len() == 0 {
			return
		}
		f := r.rec.File()
		data, err := transcript.Marshal(f)
		if err != nil {
			t.Errorf("transcript: %v", err)
			return
		}
		out := filepath.Join(dir, adapter, f.Name+".json")
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			t.Errorf("transcript: %v", err)
			return
		}
		if err := os.WriteFile(out, data, 0o600); err != nil {
			t.Errorf("transcript: %v", err)
		}
	})
	return r
}

// currentRecording is the recorder of the running test, if one was started.
func currentRecording(t *testing.T) *testRecording {
	recMu.Lock()
	defer recMu.Unlock()
	return recorders[t.Name()]
}

// recordingProxy puts a recording reverse proxy in front of a reference VTN
// on target. It returns the proxy's URL.
func recordingProxy(t *testing.T, r *testRecording, ln net.Listener, target string) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	// Compression stays as the VTN chose it: the recorder decodes it and
	// records which it was.
	proxy.Transport = r.rec.Transport(&http.Transport{DisableCompression: true, Proxy: nil})
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second}}
	srv.Start()
	t.Cleanup(srv.Close)
	r.red.Endpoint(srv.URL, "http://vtn.invalid")
	r.red.Endpoint(target, "http://vtn.invalid")
}

// selfClient marks the requests of the test to this module's VTN as the
// recording side's.
func selfClient(hc *http.Client) *http.Client {
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	c := *hc
	c.Transport = markSelf{base}
	return &c
}

type markSelf struct{ next http.RoundTripper }

func (m markSelf) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(transcript.OriginHeader, transcript.OriginSelf)
	return m.next.RoundTrip(r)
}

// recordMQTT subscribes a raw MQTT connection to topics and records every
// publication the reference VTN makes on them.
func recordMQTT(t *testing.T, clientID, username, password, uri string, topics ...string) {
	t.Helper()
	r := currentRecording(t)
	if r == nil {
		return
	}
	broker := mqtt.Options{ClientID: clientID + "-rec", Username: username, Password: password, OnMessage: r.rec.Message}
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := mqtt.Dial(c, uri, broker)
	if err != nil {
		t.Fatalf("recording MQTT: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Subscribe(c, topics...); err != nil {
		t.Fatalf("recording MQTT: %v", err)
	}
	r.red.Endpoint(uri, "mqtt://broker.invalid")
}
