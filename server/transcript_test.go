// SPDX-License-Identifier: MIT

package server_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/internal/transcript"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// Replays of recorded requests (testdata/transcripts) to this module's
// VTN. The transcripts of another implementation's clients against this
// module's VTN (ElaadNL, cB-Sigma) are replayed whole, the loading of the
// fixture included, against a new VTN configured as the recorded one was;
// every answer must be the recorded one under the comparison rules of the
// transcript package: object IDs map one to one, createdDateTime and
// modificationDateTime are date-times, tokens are non-empty.

const corpus = "../testdata/transcripts"

type noWebhooks struct{}

func (noWebhooks) Verify(context.Context, string) error { return nil }

func (noWebhooks) Send(string, string, []byte) {}

type noMQTT struct{}

func (noMQTT) Publish(string, []byte) {}

// replayServer is a VTN set up as the transcript's was.
func replayServer(t *testing.T, f transcript.File) http.Handler {
	t.Helper()
	var clients []auth.Client
	for _, c := range f.Clients {
		scopes := auth.VENScopes()
		if c.Role == "bl" {
			scopes = auth.BusinessLogicScopes()
		}
		clients = append(clients, auth.Client{ID: c.ClientID, Secret: c.SecretPlaceholder, Scopes: scopes})
	}
	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: clients})
	if err != nil {
		t.Fatal(err)
	}
	scheme := "http"
	if f.Setup["tls"] == "true" {
		scheme = "https"
	}
	cfg := server.Config{Store: storage.NewMemory(), Validator: issuer, TokenHandler: issuer,
		TokenURL: scheme + "://vtn.invalid/auth/token", AllowAnonymous: f.Setup["anonymous"] == "true", ResourceGroups: f.Setup["resourceGroups"] == "true"}
	if f.Setup["webhooks"] == "true" {
		cfg.Webhooks = noWebhooks{}
	}
	if f.Setup["mqtt"] == "true" {
		cfg.MQTT = &server.MQTTConfig{Publisher: noMQTT{}, URIs: []string{"mqtt://broker.invalid"}, TopicPrefix: f.Setup["mqttTopicPrefix"],
			Authentication: openadr.MQTTAuthentication{Method: f.Setup["mqttAuthentication"], Username: f.Setup["mqttUsername"]}}
	}
	if f.Setup["websocket"] == "true" {
		cfg.WebSocket = &server.WebSocketConfig{}
	}
	s, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// translate replaces the recorded IDs of a JSON document by the replayed
// ones, in the members that hold IDs.
func translate(v any, ids *transcript.IDMap, member string) any {
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			v[k] = translate(e, ids, k)
		}
	case []any:
		for i, e := range v {
			v[i] = translate(e, ids, member)
		}
	case string:
		if member == "id" || (strings.HasSuffix(member, "ID") && member != "clientID") || strings.HasSuffix(member, "IDs") {
			return ids.Map(v)
		}
	}
	return v
}

func translateBody(raw []byte, ids *transcript.IDMap) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw
	}
	out, err := json.Marshal(translate(v, ids, ""))
	if err != nil {
		return raw
	}
	return out
}

// TestTranscriptServerReplay replays the transcripts of other
// implementations' clients against this module's VTN.
func TestTranscriptServerReplay(t *testing.T) {
	files, err := transcript.LoadDir(corpus)
	if err != nil {
		t.Fatal(err)
	}
	var steps, peerSteps int
	for _, f := range files {
		if f.PeerRole == "VTN" || f.Setup == nil {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			n, p := replayToServer(t, f)
			steps += n
			peerSteps += p
		})
	}
	t.Logf("%d requests replayed, %d of them made by another implementation's client", steps, peerSteps)
	if steps == 0 {
		t.Fatal("nothing replayed")
	}
}

func replayToServer(t *testing.T, f transcript.File) (steps, peer int) {
	h := replayServer(t, f)
	ids := transcript.NewIDMap()
	tokens := map[string]string{} // placeholder -> token
	rules := transcript.Rules{IDs: ids, Timestamps: true, Tokens: true}
	for _, s := range f.Steps {
		if s.Transport != "http" || s.Request == nil {
			continue
		}
		where := fmt.Sprintf("step %d %s %s %s", s.N, s.Origin, s.Request.Method, s.Request.Path)
		q := s.Request
		segs := strings.Split(q.Path, "/")
		for i, seg := range segs {
			segs[i] = url.PathEscape(ids.Map(seg))
		}
		target := strings.Join(segs, "/")
		if q.Query != "" {
			vals, _ := url.ParseQuery(q.Query)
			for k, vs := range vals {
				for i, v := range vs {
					vs[i] = ids.Map(v)
				}
				vals[k] = vs
			}
			target += "?" + vals.Encode()
		}
		body := q.Bytes()
		if q.Format == transcript.FormatJSON {
			body = translateBody(body, ids)
		}
		req := httptest.NewRequest(q.Method, target, bytes.NewReader(body))
		for k, v := range q.Headers {
			if k == "Authorization" {
				if p, ok := strings.CutPrefix(v, "Bearer "); ok {
					if tok, ok := tokens[p]; ok {
						v = "Bearer " + tok
					}
				}
			}
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		steps++
		if s.Origin == transcript.OriginPeer {
			peer++
		}
		raw := rec.Body.Bytes()
		if rec.Header().Get("Content-Encoding") != "" {
			raw = decompress(t, rec.Header().Get("Content-Encoding"), raw)
		}
		want := s.Response
		if rec.Code != want.Status {
			t.Errorf("%s: status %d, recorded %d: %s", where, rec.Code, want.Status, raw)
			continue
		}
		for _, k := range []string{"Content-Type", "Content-Encoding", "WWW-Authenticate", "Location", "Allow"} {
			if got := rec.Header().Get(k); got != want.Headers[k] {
				t.Errorf("%s: %s %q, recorded %q", where, k, got, want.Headers[k])
			}
		}
		if q.Route == "/auth/token" && rec.Code == http.StatusOK {
			var w, g struct {
				AccessToken string `json:"access_token"`
			}
			_ = json.Unmarshal(want.JSON, &w)
			_ = json.Unmarshal(raw, &g)
			tokens[w.AccessToken] = g.AccessToken
		}
		if want.Format == transcript.FormatJSON {
			for _, d := range transcript.Compare(want.Bytes(), raw, rules) {
				t.Errorf("%s: %s", where, d)
			}
		} else if want.Format == transcript.FormatText && strings.TrimSpace(want.Text) != strings.TrimSpace(string(raw)) {
			t.Errorf("%s: body %q, recorded %q", where, raw, want.Text)
		}
	}
	return steps, peer
}

func decompress(t *testing.T, encoding string, raw []byte) []byte {
	t.Helper()
	if encoding != "gzip" {
		t.Fatalf("Content-Encoding %s", encoding)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
