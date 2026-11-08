// SPDX-License-Identifier: MIT

package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/otfabric/go-openadr/internal/transcript"
)

// The differential replay: the requests of a transcript recorded against
// another implementation's VTN of OpenADR 3.1.0 are sent to this module's
// VTN, serving the same fixture, and its answers compared with the other
// VTN's. Two VTNs may answer one request differently and both be right:
// every difference in status is classified in statusDifferences, and the
// test fails on one that is not.

type fixtureDoc struct {
	Clients []struct {
		Name, Role, ClientID, ClientSecret string
	} `json:"clients"`
	VENs []struct {
		VENName string          `json:"venName"`
		Client  string          `json:"client"`
		Targets json.RawMessage `json:"targets"`
	} `json:"vens"`
	Resources []struct {
		ResourceName string `json:"resourceName"`
		VEN          string `json:"ven"`
	} `json:"resources"`
	Programs []map[string]any `json:"programs"`
	Events   []map[string]any `json:"events"`
}

// differentialServer is a VTN of this module with the fixture of a
// transcript loaded as openadr-interop loads it, through the API as
// Business Logic. It returns the names of the objects by ID.
func differentialServer(t *testing.T, f transcript.File) (http.Handler, map[string]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpus, "fixtures", f.Fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx fixtureDoc
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	g := f
	g.Setup = map[string]string{"webhooks": "true", "mqtt": "true", "websocket": "true", "resourceGroups": "true"}
	h := replayServer(t, g)
	tok := tokenFor(t, h, fx.Clients[0].ClientID, fx.Clients[0].ClientSecret)
	names := map[string]string{}
	post := func(path string, body any) string {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var obj struct {
			ID string `json:"id"`
		}
		if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &obj) != nil {
			t.Fatalf("loading the fixture: POST %s: %d %s", path, rec.Code, rec.Body)
		}
		return obj.ID
	}
	clientID := map[string]string{}
	for _, c := range fx.Clients {
		clientID[c.Name] = c.ClientID
	}
	venIDs, programIDs := map[string]string{}, map[string]string{}
	for _, v := range fx.VENs {
		body := map[string]any{"objectType": "BL_VEN_REQUEST", "venName": v.VENName, "clientID": clientID[v.Client]}
		if len(v.Targets) > 0 {
			body["targets"] = v.Targets
		}
		venIDs[v.VENName] = post("/vens", body)
		names[venIDs[v.VENName]] = "ven:" + v.VENName
	}
	for _, r := range fx.Resources {
		var client string
		for _, v := range fx.VENs {
			if v.VENName == r.VEN {
				client = clientID[v.Client]
			}
		}
		id := post("/resources", map[string]any{"objectType": "BL_RESOURCE_REQUEST", "resourceName": r.ResourceName, "venID": venIDs[r.VEN], "clientID": client})
		names[id] = "resource:" + r.ResourceName
	}
	for _, p := range fx.Programs {
		name := p["programName"].(string)
		programIDs[name] = post("/programs", p)
		names[programIDs[name]] = "program:" + name
	}
	for _, e := range fx.Events {
		e["programID"] = programIDs[e["program"].(string)]
		delete(e, "program")
		id := post("/events", e)
		names[id] = "event:" + e["eventName"].(string)
	}
	return h, names
}

func tokenFor(t *testing.T, h http.Handler, id, secret string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("no token for %s: %d %s", id, rec.Code, rec.Body)
	}
	return tok.AccessToken
}

// namesIn collects, from a recorded body, the ID of each named object.
func namesIn(raw []byte, into map[string]string) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			id, _ := v["id"].(string)
			for member, kind := range map[string]string{"programName": "program", "eventName": "event", "venName": "ven", "resourceName": "resource"} {
				if n, ok := v[member].(string); ok && id != "" {
					into[kind+":"+n] = id
				}
			}
		}
	}
	walk(v)
}

type differential struct {
	step          transcript.Step
	recorded, got int
	gotBody       string
	bodyDiffs     []transcript.Diff
}

// replayDifferential sends the requests of a transcript to a VTN of this
// module and returns, for every step, the recorded and the replayed answer.
func replayDifferential(t *testing.T, f transcript.File) []differential {
	h, ours := differentialServer(t, f)
	ids := transcript.NewIDMap()
	// The fixture's objects, by name, in the recording.
	theirs := map[string]string{}
	for _, s := range f.Steps {
		if s.Response != nil && s.Response.Format == transcript.FormatJSON {
			namesIn(s.Response.JSON, theirs)
		}
	}
	for id, name := range ours {
		if their, ok := theirs[name]; ok {
			ids.Match(their, id)
		}
	}
	tokens := map[string]string{}
	var out []differential
	for _, s := range f.Steps {
		if s.Transport != "http" || s.Request == nil || s.Origin != transcript.OriginSelf {
			continue
		}
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
			if k == "Accept-Encoding" {
				continue
			}
			if p, ok := strings.CutPrefix(v, "Bearer "); ok && k == "Authorization" {
				if tok, ok := tokens[p]; ok {
					v = "Bearer " + tok
				}
			}
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		d := differential{step: s, recorded: s.Response.Status, got: rec.Code, gotBody: rec.Body.String()}
		// The headers HTTP requires with a status: WWW-Authenticate with
		// 401 (RFC 9110 section 15.5.2), Allow with 405 (section 15.5.6).
		for _, h := range []string{"WWW-Authenticate", "Allow"} {
			if s.Response.Headers[h] != "" && rec.Code == s.Response.Status && rec.Header().Get(h) == "" {
				d.bodyDiffs = append(d.bodyDiffs, transcript.Diff{Path: "header " + h, Kind: transcript.DiffMissing, Want: s.Response.Headers[h], Got: "(absent)"})
			}
		}
		if q.Route == "/auth/token" && rec.Code == http.StatusOK && s.Response.Status == http.StatusOK {
			var w, g struct {
				AccessToken string `json:"access_token"`
			}
			_ = json.Unmarshal(s.Response.JSON, &w)
			_ = json.Unmarshal(rec.Body.Bytes(), &g)
			// A VTN that answers a second request of a client with the
			// token it gave before (OpenLEADR) has one placeholder for
			// both: the first token stands for it.
			if _, ok := tokens[w.AccessToken]; !ok {
				tokens[w.AccessToken] = g.AccessToken
			}
		}
		if rec.Code/100 == 2 && s.Response.Status/100 == 2 && s.Response.Format == transcript.FormatJSON {
			// OpenADR 3 does not say in which order a search returns its
			// objects: both lists are put in the order of their names.
			d.bodyDiffs = transcript.Compare(byName(s.Response.Bytes()), byName(rec.Body.Bytes()), transcript.Rules{IDs: ids, Timestamps: true, Tokens: true, Equivalent: true})
		}
		out = append(out, d)
	}
	return out
}

// byName orders a JSON list of objects by their name member.
func byName(raw []byte) []byte {
	var list []map[string]json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return raw
	}
	name := func(o map[string]json.RawMessage) string {
		for _, m := range []string{"programName", "eventName", "venName", "resourceName", "reportName", "clientName"} {
			if v, ok := o[m]; ok {
				return string(v)
			}
		}
		return ""
	}
	sort.SliceStable(list, func(i, j int) bool { return name(list[i]) < name(list[j]) })
	out, err := json.Marshal(list)
	if err != nil {
		return raw
	}
	return out
}

// differentialKnown classifies the steps where this module's VTN answers
// otherwise than the recorded one: by source and the step's note, a
// trailing * matching any rest. status is this module's status where the
// statuses differ, 0 where only the bodies do.
type knownDifferential struct {
	status     int
	class, why string
}

const (
	whyOrder      = "OpenADR 3 does not say in which order a search returns objects; pages of differently ordered lists hold different objects"
	whyTargets    = "this module shows a targeted object to a VEN only when its search names a target the VEN was granted; cB-Sigma's VTN (serverTargetsUnlock) and OpenLEADR show it either way (docs/CONFORMANCE.md)"
	whyScope      = "the token answer's scope is the VTN's own: this module names the scopes it granted, OpenLEADR gives none, cB-Sigma other names"
	whyNotifiers  = "the replay's VTN is configured with MQTT (anonymous) and WebSockets; the recorded VTNs differ in what they offer"
	whyNoMQTT     = "cB-Sigma's VTN has no MQTT notifier in openadr-interop (not wired), and answers 404"
	whyTopicNames = "MQTT topic names are the VTN's own choice (notifierTopicsResponse); OpenLEADR v0.2.8 names them differently"
	whyNoVerify   = "the replay's VTN has a webhook sender that accepts every callback; cB-Sigma's VTN verified http://callback.invalid and could not reach it (400), so the later steps on that subscription find none"
	whyPathID     = "an ID with characters the objectID pattern does not allow is answered 400 by this module (and OpenLEADR), 404 by cB-Sigma"
	whyMethod     = "a method the path does not have is answered 405 by this module (and OpenLEADR), 404 by cB-Sigma"
	whyForeignKey = "a reference to an object that does not exist is a conflict (409) for this module and OpenLEADR, a bad request (400) for cB-Sigma"
	whyNoCT       = "a request body without Content-Type is read as JSON (RFC 9110 section 8.3); OpenLEADR answers 415, cB-Sigma 400"
	whyTextPlain  = "a body declared text/plain is refused as 415 Unsupported Media Type, as OpenLEADR does; cB-Sigma answers 400"
	whyRead       = "READ is an operation of objectOperations in OpenADR 3.1.0 (enum READ, CREATE, UPDATE, DELETE); OpenLEADR v0.2.8 and cB-Sigma v0.2.0 refuse it"
	whyOtherVEN   = "a VEN cannot add a resource to another client's ven: this module does not show it the ven (409, no such ven), cB-Sigma answers 403, OpenLEADR v0.2.8 creates it (201)"
)

var differentialKnown = map[string]knownDifferential{
	// cB-Sigma openadr v0.2.0.
	"cbsigma|programs as ven-1, its target":        {0, transcript.ClassVariation, whyTargets},
	"cbsigma|programs as ven-2, ven-1's target":    {0, transcript.ClassVariation, whyTargets},
	"cbsigma|events as ven-1, its target":          {0, transcript.ClassVariation, whyTargets},
	"cbsigma|notifiers":                            {0, transcript.ClassVendor, whyNotifiers},
	"cbsigma|subscriptions as ven-1":               {0, transcript.ClassIncomplete, whyNoVerify},
	"cbsigma|subscriptions of the program as BL":   {0, transcript.ClassIncomplete, whyNoVerify},
	"cbsigma|token for bl":                         {0, transcript.ClassVendor, whyScope},
	"cbsigma|token: HTTP Basic credentials":        {0, transcript.ClassVendor, whyScope},
	"cbsigma|DELETE the program collection":        {405, transcript.ClassVendor, whyMethod},
	"cbsigma|PATCH program":                        {405, transcript.ClassVendor, whyMethod},
	"cbsigma|token: GET":                           {405, transcript.ClassVendor, whyMethod},
	"cbsigma|create event, unknown program":        {409, transcript.ClassVariation, whyForeignKey},
	"cbsigma|create report, unknown event":         {409, transcript.ClassVariation, whyForeignKey},
	"cbsigma|create program, as text/plain":        {415, transcript.ClassVariation, whyTextPlain},
	"cbsigma|create program, no content type":      {201, transcript.ClassVariation, whyNoCT},
	"cbsigma|create subscription":                  {201, transcript.ClassIncomplete, whyNoVerify},
	"cbsigma|create subscription, operation READ":  {201, transcript.ClassVendor, whyRead},
	"cbsigma|read subscription":                    {400, transcript.ClassIncomplete, whyNoVerify},
	"cbsigma|delete subscription":                  {400, transcript.ClassIncomplete, whyNoVerify},
	"cbsigma|events, active only":                  {200, transcript.ClassVendor, "activeOnly is a query parameter of GET /events in OpenADR 3.1.0; cB-Sigma v0.2.0 refuses it (400)"},
	"cbsigma|read program, ID with bad characters": {400, transcript.ClassVariation, whyPathID},
	"cbsigma|token: no client_id": {401, transcript.ClassVariation,
		"a token request without client credentials is invalid_client (401) for this module and OpenLEADR, invalid_request (400) for cB-Sigma; RFC 6749 section 5.2 allows either reading"},
	"cbsigma|topics *": {200, transcript.ClassVendor, whyNoMQTT},
	"cbsigma|ven-2 creates a resource for ven-1's ven": {409, transcript.ClassVariation, whyOtherVEN},

	// OpenLEADR v0.2.8.
	"openleadr|programs as ven-1": {0, transcript.ClassVariation, whyTargets},
	"openleadr|events as ven-1":   {0, transcript.ClassVariation, whyTargets},
	"openleadr|paging *":          {0, transcript.ClassVariation, whyOrder},
	"openleadr|notifiers":         {0, transcript.ClassVendor, whyNotifiers + "; OpenLEADR names its MQTT authentication OAUTH2BEARERTOKEN where 3.1.0 has OAUTH2_BEARER_TOKEN"},
	"openleadr|token*":            {0, transcript.ClassVendor, whyScope},
	"openleadr|topics /notifiers/mqtt/topics/programs/{prog}/events": {0, transcript.ClassVendor, whyTopicNames},
	"openleadr|topics /notifiers/mqtt/topics/vens/*":                 {0, transcript.ClassVendor, whyTopicNames},
	"openleadr|ven-1 creates a report":                               {0, transcript.ClassVendor, "OpenLEADR v0.2.8 drops readingType from a report's payload descriptors; this module keeps it"},
	"openleadr|read report":                                          {0, transcript.ClassVendor, "OpenLEADR v0.2.8 drops readingType from a report's payload descriptors; this module keeps it"},
	"openleadr|reports *":                                            {0, transcript.ClassVendor, "OpenLEADR v0.2.8 drops readingType from a report's payload descriptors; this module keeps it"},
	"openleadr|create subscription":                                  {0, transcript.ClassVendor, "OpenLEADR v0.2.8 drops the targets of a subscription; this module keeps them"},
	"openleadr|read subscription":                                    {0, transcript.ClassVendor, "OpenLEADR v0.2.8 drops the targets of a subscription; this module keeps them"},
	"openleadr|subscriptions *":                                      {0, transcript.ClassVendor, "OpenLEADR v0.2.8 drops the targets of a subscription; this module keeps them"},
	"openleadr|create program, no content type":                      {201, transcript.ClassVariation, whyNoCT},
	"openleadr|create subscription, no callback": {400, transcript.ClassVendor,
		"callbackUrl is required in objectOperations (OpenADR 3.1.0); OpenLEADR v0.2.8 creates the subscription without one"},
	"openleadr|create subscription, operation READ": {201, transcript.ClassVendor, whyRead},
	"openleadr|paging limit=0":                      {200, transcript.ClassVendor, "limit has minimum 0 in OpenADR 3.1.0; this module returns no objects, OpenLEADR v0.2.8 refuses it (400)"},
	"openleadr|token: a JSON body": {400, transcript.ClassVariation,
		"a token request must be a form (RFC 6749 section 4.4.2); this module answers invalid_request (400), OpenLEADR 415"},
	"openleadr|topics /notifiers/mqtt/topics/subscriptions": {200, transcript.ClassVendor,
		"OpenADR 3.1.0 has GET /notifiers/mqtt/topics/subscriptions; OpenLEADR v0.2.8 answers 404"},
	"openleadr|topics of another VEN, as ven-2": {404, transcript.ClassVariation,
		"the topics of a ven are the VEN's own (VEN-scoped topics, 3.1.0): this module does not show another VEN's ven, OpenLEADR v0.2.8 answers them"},
	"openleadr|ven-2 creates a resource for ven-1's ven": {409, transcript.ClassVariation, whyOtherVEN},
	"openleadr|ven-3 registers a second ven": {201, transcript.ClassVariation,
		"OpenADR 3.1.0 does not limit a client to one ven; OpenLEADR v0.2.8 does (409)"},
	"openleadr|ven-3 uses BL_VEN_REQUEST": {403, transcript.ClassVariation,
		"a VEN that sends a BL_VEN_REQUEST is refused: 403 by this module and cB-Sigma, 400 by OpenLEADR"},
}

func lookupDifferential(source, note string) (string, knownDifferential, bool) {
	if k, ok := differentialKnown[source+"|"+note]; ok {
		return source + "|" + note, k, true
	}
	for key, k := range differentialKnown {
		if prefix, ok := strings.CutSuffix(key, "*"); ok && strings.HasPrefix(source+"|"+note, prefix) {
			return key, k, true
		}
	}
	return "", knownDifferential{}, false
}

// TestTranscriptDifferential replays the scenarios recorded against other
// implementations' VTNs of OpenADR 3.1.0 to this module's VTN. Every
// step where the two answer differently must be classified in
// differentialKnown, and every entry there must be used.
func TestTranscriptDifferential(t *testing.T) {
	files, err := transcript.LoadDir(corpus)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	var steps, same int
	for _, f := range files {
		if f.PeerRole != "VTN" || f.ProtocolVersion != "3.1.0" || !strings.HasSuffix(f.Name, "_scenario") {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			for _, d := range replayDifferential(t, f) {
				steps++
				sh, _ := transcript.ResponseShape(d.step)
				var bodyDiffs []transcript.Diff
				for _, bd := range d.bodyDiffs {
					if _, ok := transcript.Known(f.Source, sh.Kind, bd); !ok {
						bodyDiffs = append(bodyDiffs, bd)
					}
				}
				if d.recorded == d.got && len(bodyDiffs) == 0 {
					same++
					continue
				}
				where := fmt.Sprintf("step %d %s %s?%s (%s)", d.step.N, d.step.Request.Method, d.step.Request.Path, d.step.Request.Query, d.step.Note)
				key, k, ok := lookupDifferential(f.Source, d.step.Note)
				switch {
				case !ok:
					t.Errorf("%s: %s %d, this module %d, unclassified: %.300s; body %v", where, f.Source, d.recorded, d.got, d.gotBody, bodyDiffs)
				case d.recorded != d.got && k.status != d.got:
					t.Errorf("%s: %s %d, this module %d; the table says %d (%s)", where, f.Source, d.recorded, d.got, k.status, k.why)
				case d.recorded == d.got && k.status != 0:
					t.Errorf("%s: both %d; the table says this module answers %d", where, d.got, k.status)
				default:
					used[key] = true
				}
			}
		})
	}
	for key := range differentialKnown {
		if !used[key] {
			t.Errorf("differentialKnown[%q] matches no step", key)
		}
	}
	t.Logf("%d requests replayed, %d answered alike, %d differently as classified", steps, same, steps-same)
}
