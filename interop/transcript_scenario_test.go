//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/internal/transcript"
)

// The transcript scenarios: requests written by hand, sent to a reference
// VTN through the recording proxy, to record how that VTN answers what the
// interop tests do not ask: paging, malformed and invalid input, access
// without or with a wrong token, the token endpoint's errors, content
// types, and the lifecycle of every kind of object. They assert nothing
// about the answers, which are the evidence; they only run while recording
// (OPENADR_TRANSCRIPT_RECORD).

func TestTranscriptScenarioOpenLEADR(t *testing.T) { transcriptScenario(t, "openleadr", "baseline") }
func TestTranscriptScenarioCbsigma(t *testing.T)   { transcriptScenario(t, "cbsigma", "baseline") }
func TestTranscriptScenarioOpenLEADR301(t *testing.T) {
	transcriptScenario(t, "openleadr301", "baseline-301")
}

type scenario struct {
	t      *testing.T
	base   string
	hc     *http.Client
	fx     fixture
	tokens map[string]string
	vars   map[string]string
	nonce  string
}

func transcriptScenario(t *testing.T, adapter, fixtureName string) {
	if os.Getenv("OPENADR_TRANSCRIPT_RECORD") == "" {
		t.Skip("records a transcript only: set OPENADR_TRANSCRIPT_RECORD")
	}
	base := startServer(t, adapter, fixtureName)
	s := &scenario{t: t, base: base, hc: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		fx: readFixture(t, image(adapter), fixtureName), tokens: map[string]string{}, vars: map[string]string{},
		nonce: fmt.Sprintf("%x", time.Now().UnixNano()%0xffffff)}
	if adapter == "openleadr301" {
		s.run301()
		return
	}
	s.run310()
}

// expand replaces {name} by a value the scenario captured.
func (s *scenario) expand(p string) string {
	for k, v := range s.vars {
		p = strings.ReplaceAll(p, "{"+k+"}", v)
	}
	return strings.ReplaceAll(p, "{nonce}", s.nonce)
}

// do sends one request. as is a client of the fixture, "" for none, or
// "bad" for a token the VTN never issued.
func (s *scenario) do(note, as, method, path, contentType, body string, header ...string) (int, []byte) {
	s.t.Helper()
	path, body = s.expand(path), s.expand(body)
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.base+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set(transcript.NoteHeader, note)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	switch as {
	case "":
	case "bad":
		req.Header.Set("Authorization", "Bearer not-a-token-the-vtn-issued")
	default:
		req.Header.Set("Authorization", "Bearer "+s.token(as))
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// token gets a token for a client, once.
func (s *scenario) token(name string) string {
	s.t.Helper()
	if tok, ok := s.tokens[name]; ok {
		return tok
	}
	id, secret := s.fx.credentials(s.t, name)
	status, raw := s.do("token for "+name, "", http.MethodPost, "/auth/token", "application/x-www-form-urlencoded",
		url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}}.Encode())
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &tok) != nil || tok.AccessToken == "" {
		s.t.Fatalf("no token for %s: %d %s", name, status, raw)
	}
	s.tokens[name] = tok.AccessToken
	return tok.AccessToken
}

// capture keeps the id of a created object, or a member of the first object
// of a list, as {name}.
func (s *scenario) capture(name string, status int, raw []byte, member string) {
	s.t.Helper()
	var obj map[string]any
	var list []map[string]any
	if json.Unmarshal(raw, &obj) == nil && obj != nil {
		if v, ok := obj[member].(string); ok {
			s.vars[name] = v
			return
		}
	}
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		if v, ok := list[0][member].(string); ok {
			s.vars[name] = v
			return
		}
	}
	s.t.Logf("nothing to capture as %s: %d %s", name, status, raw)
}

// find keeps the id of the object of a list with name in member.
func (s *scenario) find(name string, raw []byte, member, value string) {
	var list []map[string]any
	_ = json.Unmarshal(raw, &list)
	for _, o := range list {
		if o[member] == value {
			if id, ok := o["id"].(string); ok {
				s.vars[name] = id
				return
			}
		}
	}
	s.t.Logf("no %s %q to capture as %s", member, value, name)
}

const form = "application/x-www-form-urlencoded"
const js = "application/json"

func (s *scenario) authErrors() {
	blID, blSecret := s.fx.credentials(s.t, "bl")
	venID, _ := s.fx.credentials(s.t, "ven-1")
	s.do("token: wrong secret", "", "POST", "/auth/token", form, url.Values{"grant_type": {"client_credentials"}, "client_id": {venID}, "client_secret": {"not-the-secret"}}.Encode())
	s.do("token: unknown client", "", "POST", "/auth/token", form, url.Values{"grant_type": {"client_credentials"}, "client_id": {"no-such-client"}, "client_secret": {"not-the-secret"}}.Encode())
	s.do("token: unsupported grant type", "", "POST", "/auth/token", form, url.Values{"grant_type": {"password"}, "client_id": {blID}, "client_secret": {blSecret}}.Encode())
	s.do("token: no grant type", "", "POST", "/auth/token", form, url.Values{"client_id": {blID}, "client_secret": {blSecret}}.Encode())
	s.do("token: no client_id", "", "POST", "/auth/token", form, url.Values{"grant_type": {"client_credentials"}}.Encode())
	s.do("token: HTTP Basic credentials", "", "POST", "/auth/token", form, url.Values{"grant_type": {"client_credentials"}}.Encode(),
		"Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(blID)+":"+url.QueryEscape(blSecret))))
	s.do("token: a JSON body", "", "POST", "/auth/token", js, fmt.Sprintf(`{"grant_type":"client_credentials","client_id":%q,"client_secret":%q}`, blID, blSecret))
	s.do("token: GET", "", "GET", "/auth/token", "", "")
	s.do("token: with a scope", "", "POST", "/auth/token", form, url.Values{"grant_type": {"client_credentials"}, "client_id": {blID}, "client_secret": {blSecret}, "scope": {"read_all"}}.Encode())
}

func (s *scenario) paging(collection string) {
	for _, q := range []string{"limit=1", "skip=1&limit=1", "skip=1", "skip=1000", "limit=0", "limit=50", "limit=51", "limit=-1", "skip=-1", "limit=abc", "skip=abc"} {
		s.do("paging "+q, "bl", "GET", "/"+collection+"?"+q, "", "")
	}
}

func (s *scenario) run310() {
	s.do("server info, no token", "", "GET", "/auth/server", "", "")
	for _, c := range []string{"bl", "ven-1", "ven-2", "ven-3"} {
		s.token(c)
	}
	s.authErrors()

	// Access.
	s.do("programs without a token", "", "GET", "/programs", "", "")
	s.do("programs with a token the VTN never issued", "bad", "GET", "/programs", "", "")
	s.do("programs with a malformed Authorization header", "", "GET", "/programs", "", "", "Authorization", "Token abc")
	_, raw := s.do("programs as BL", "bl", "GET", "/programs", "", "")
	s.find("dayAhead", raw, "programName", "day-ahead")
	s.find("groupA", raw, "programName", "group-a-flex")
	s.do("programs as ven-1", "ven-1", "GET", "/programs", "", "")
	s.do("programs as ven-1, its target", "ven-1", "GET", "/programs?targets=group-a", "", "")
	s.do("programs as ven-2, ven-1's target", "ven-2", "GET", "/programs?targets=group-a", "", "")
	s.do("programs as ven-3 (no ven object)", "ven-3", "GET", "/programs", "", "")
	s.do("targeted program by ID as ven-2", "ven-2", "GET", "/programs/{groupA}", "", "")
	s.do("programs, two targets", "bl", "GET", "/programs?targets=group-a&targets=group-b", "", "")
	s.do("programs, Accept XML", "bl", "GET", "/programs", "", "", "Accept", "application/xml")
	s.paging("programs")

	// Program lifecycle and invalid input.
	status, raw := s.do("create program", "bl", "POST", "/programs", js, `{"programName":"transcript-{nonce}","intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"P1D"},"programDescriptions":[{"URL":"https://example.com/program"}],"payloadDescriptors":[{"objectType":"EVENT_PAYLOAD_DESCRIPTOR","payloadType":"PRICE","units":"KWH","currency":"EUR"}],"attributes":[{"type":"LOCATION","values":["NL"]}],"targets":["transcript-group"]}`)
	s.capture("prog", status, raw, "id")
	s.do("read program", "bl", "GET", "/programs/{prog}", "", "")
	s.do("update program", "bl", "PUT", "/programs/{prog}", js, `{"programName":"transcript-{nonce}","intervalPeriod":{"start":"2030-01-02T00:00:00Z","duration":"P2D"},"targets":["transcript-group"]}`)
	s.do("create program, name taken", "bl", "POST", "/programs", js, `{"programName":"transcript-{nonce}"}`)
	s.do("create program as a VEN", "ven-1", "POST", "/programs", js, `{"programName":"transcript-ven-{nonce}"}`)
	s.do("create program, malformed JSON", "bl", "POST", "/programs", js, `{"programName":`)
	s.do("create program, no programName", "bl", "POST", "/programs", js, `{}`)
	s.do("create program, programName a number", "bl", "POST", "/programs", js, `{"programName":5}`)
	s.do("create program, programName null", "bl", "POST", "/programs", js, `{"programName":null}`)
	s.do("create program, empty programName", "bl", "POST", "/programs", js, `{"programName":""}`)
	s.do("create program, a bad duration", "bl", "POST", "/programs", js, `{"programName":"transcript-dur-{nonce}","intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"one day"}}`)
	s.do("create program, a bad date-time", "bl", "POST", "/programs", js, `{"programName":"transcript-dt-{nonce}","intervalPeriod":{"start":"tomorrow","duration":"P1D"}}`)
	status, raw = s.do("create program, an unknown member", "bl", "POST", "/programs", js, `{"programName":"transcript-extra-{nonce}","noSuchMember":1}`)
	s.capture("extra", status, raw, "id")
	s.do("create program, as text/plain", "bl", "POST", "/programs", "text/plain", `{"programName":"transcript-text-{nonce}"}`)
	s.do("create program, no content type", "bl", "POST", "/programs", "", `{"programName":"transcript-noct-{nonce}"}`)
	s.do("create program, a JSON array", "bl", "POST", "/programs", js, `[{"programName":"transcript-array-{nonce}"}]`)
	s.do("read unknown program", "bl", "GET", "/programs/no-such-program", "", "")
	s.do("read program, ID with bad characters", "bl", "GET", "/programs/bad%20id%21", "", "")
	s.do("PATCH program", "bl", "PATCH", "/programs/{prog}", js, `{"programName":"x"}`)
	s.do("DELETE the program collection", "bl", "DELETE", "/programs", "", "")
	s.do("unknown path", "bl", "GET", "/no-such-thing", "", "")

	// Events.
	status, raw = s.do("create event", "bl", "POST", "/events", js, `{"programID":"{prog}","eventName":"transcript-event-{nonce}","priority":2,"targets":["transcript-group"],"reportDescriptors":[{"payloadType":"USAGE","readingType":"DIRECT_READ","units":"KWH","aggregate":false,"startInterval":-1,"numIntervals":-1,"historical":true,"frequency":-1,"repeat":1}],"payloadDescriptors":[{"objectType":"EVENT_PAYLOAD_DESCRIPTOR","payloadType":"PRICE","units":"KWH","currency":"EUR"}],"intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"PT1H","randomizeStart":"PT5M"},"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[0.17]}]},{"id":1,"intervalPeriod":{"start":"2030-01-01T01:00:00Z","duration":"PT1H"},"payloads":[{"type":"PRICE","values":[0.21]},{"type":"SIMPLE","values":[1,2]},{"type":"CUSTOM_TEXT","values":["text",true]},{"type":"CUSTOM_POINT","values":[{"x":1.5,"y":2.5}]}]}]}`)
	s.capture("ev", status, raw, "id")
	s.do("events of the program", "bl", "GET", "/events?programID={prog}", "", "")
	s.do("read event", "bl", "GET", "/events/{ev}", "", "")
	s.do("update event", "bl", "PUT", "/events/{ev}", js, `{"programID":"{prog}","eventName":"transcript-event-{nonce}","priority":3,"targets":["transcript-group"],"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[0.5]}]}]}`)
	s.do("create event, unknown program", "bl", "POST", "/events", js, `{"programID":"no-such-program","intervals":[]}`)
	status, raw = s.do("create event, no intervals", "bl", "POST", "/events", js, `{"programID":"{prog}","eventName":"transcript-noint-{nonce}"}`)
	s.capture("ev2", status, raw, "id")
	s.do("create event, an interval without payloads", "bl", "POST", "/events", js, `{"programID":"{prog}","intervals":[{"id":0}]}`)
	s.do("create event as a VEN", "ven-1", "POST", "/events", js, `{"programID":"{prog}","intervals":[]}`)
	s.do("events as ven-1", "ven-1", "GET", "/events", "", "")
	s.do("events as ven-1, its target", "ven-1", "GET", "/events?targets=group-a", "", "")
	s.do("events, active only", "bl", "GET", "/events?activeOnly=true", "", "")
	s.paging("events")

	// VENs and resources.
	_, raw = s.do("vens as BL", "bl", "GET", "/vens", "", "")
	s.find("site1", raw, "venName", "site-1")
	s.do("vens as ven-1", "ven-1", "GET", "/vens", "", "")
	s.do("vens by name", "bl", "GET", "/vens?venName=site-1", "", "")
	s.do("vens by target", "bl", "GET", "/vens?targets=group-a", "", "")
	s.do("another VEN's ven, as ven-2", "ven-2", "GET", "/vens/{site1}", "", "")
	status, raw = s.do("create ven as BL", "bl", "POST", "/vens", js, `{"objectType":"BL_VEN_REQUEST","clientID":"transcript-client-{nonce}","venName":"transcript-ven-{nonce}","targets":["transcript-group"],"attributes":[{"type":"LOCATION","values":["NL"]}]}`)
	s.capture("ven", status, raw, "id")
	s.do("create ven as BL, no objectType", "bl", "POST", "/vens", js, `{"clientID":"transcript-client2-{nonce}","venName":"transcript-ven2-{nonce}"}`)
	s.do("create ven as BL, name taken", "bl", "POST", "/vens", js, `{"objectType":"BL_VEN_REQUEST","clientID":"transcript-client3-{nonce}","venName":"transcript-ven-{nonce}"}`)
	s.do("read ven", "bl", "GET", "/vens/{ven}", "", "")
	s.do("update ven", "bl", "PUT", "/vens/{ven}", js, `{"objectType":"BL_VEN_REQUEST","clientID":"transcript-client-{nonce}","venName":"transcript-ven-{nonce}","targets":["transcript-group","other"]}`)
	status, raw = s.do("ven-3 registers itself", "ven-3", "POST", "/vens", js, `{"objectType":"VEN_VEN_REQUEST","venName":"transcript-self-{nonce}"}`)
	s.capture("selfVen", status, raw, "id")
	s.do("ven-3 registers a second ven", "ven-3", "POST", "/vens", js, `{"objectType":"VEN_VEN_REQUEST","venName":"transcript-self2-{nonce}"}`)
	s.do("ven-3 uses BL_VEN_REQUEST", "ven-3", "POST", "/vens", js, `{"objectType":"BL_VEN_REQUEST","clientID":"other","venName":"transcript-other-{nonce}"}`)
	s.do("resources as ven-1", "ven-1", "GET", "/resources", "", "")
	s.do("resources of site-1 as BL", "bl", "GET", "/resources?venID={site1}", "", "")
	status, raw = s.do("ven-1 creates a resource", "ven-1", "POST", "/resources", js, `{"objectType":"VEN_RESOURCE_REQUEST","resourceName":"transcript-res-{nonce}","venID":"{site1}","attributes":[{"type":"MAX_POWER_CONSUMPTION","values":[11]}]}`)
	s.capture("res", status, raw, "id")
	s.do("read resource", "ven-1", "GET", "/resources/{res}", "", "")
	s.do("update resource", "ven-1", "PUT", "/resources/{res}", js, `{"objectType":"VEN_RESOURCE_REQUEST","resourceName":"transcript-res-{nonce}","venID":"{site1}"}`)
	s.do("ven-2 reads ven-1's resource", "ven-2", "GET", "/resources/{res}", "", "")
	s.do("ven-2 creates a resource for ven-1's ven", "ven-2", "POST", "/resources", js, `{"objectType":"VEN_RESOURCE_REQUEST","resourceName":"transcript-res2-{nonce}","venID":"{site1}"}`)
	status, raw = s.do("BL creates a resource", "bl", "POST", "/resources", js, `{"objectType":"BL_RESOURCE_REQUEST","clientID":"interop-ven-1","resourceName":"transcript-blres-{nonce}","venID":"{site1}","targets":["transcript-group"]}`)
	s.capture("blres", status, raw, "id")
	s.paging("resources")

	// Reports.
	status, raw = s.do("ven-1 creates a report", "ven-1", "POST", "/reports", js, `{"eventID":"{ev}","clientName":"site-1","reportName":"transcript-report-{nonce}","payloadDescriptors":[{"objectType":"REPORT_PAYLOAD_DESCRIPTOR","payloadType":"USAGE","readingType":"DIRECT_READ","units":"KWH","accuracy":0.5,"confidence":90}],"resources":[{"resourceName":"battery","intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"PT1H"},"intervals":[{"id":0,"payloads":[{"type":"USAGE","values":[1.25]}]}]}]}`)
	s.capture("rep", status, raw, "id")
	s.do("reports of the event as ven-1", "ven-1", "GET", "/reports?eventID={ev}", "", "")
	s.do("reports of the program as BL", "bl", "GET", "/reports?programID={prog}", "", "")
	s.do("reports by client name", "bl", "GET", "/reports?clientName=site-1", "", "")
	s.do("read report", "ven-1", "GET", "/reports/{rep}", "", "")
	s.do("ven-2 reads ven-1's report", "ven-2", "GET", "/reports/{rep}", "", "")
	s.do("update report", "ven-1", "PUT", "/reports/{rep}", js, `{"eventID":"{ev}","clientName":"site-1","reportName":"transcript-report-{nonce}","resources":[{"resourceName":"battery","intervals":[{"id":0,"payloads":[{"type":"USAGE","values":[2.5]}]}]}]}`)
	s.do("create report, unknown event", "ven-1", "POST", "/reports", js, `{"eventID":"no-such-event","clientName":"site-1","resources":[]}`)
	s.do("create report as BL", "bl", "POST", "/reports", js, `{"eventID":"{ev}","clientName":"site-1","resources":[]}`)
	s.do("delete report", "ven-1", "DELETE", "/reports/{rep}", "", "")

	// Subscriptions.
	status, raw = s.do("create subscription", "ven-1", "POST", "/subscriptions", js, `{"clientName":"site-1","programID":"{prog}","objectOperations":[{"objects":["EVENT","PROGRAM"],"operations":["CREATE","UPDATE","DELETE"],"callbackUrl":"http://callback.invalid/notifications","bearerToken":"transcript-callback-token"}],"targets":["transcript-group"]}`)
	s.capture("sub", status, raw, "id")
	s.do("subscriptions as ven-1", "ven-1", "GET", "/subscriptions", "", "")
	s.do("subscriptions of the program as BL", "bl", "GET", "/subscriptions?programID={prog}", "", "")
	s.do("read subscription", "ven-1", "GET", "/subscriptions/{sub}", "", "")
	s.do("update subscription", "ven-1", "PUT", "/subscriptions/{sub}", js, `{"clientName":"site-1","programID":"{prog}","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"],"callbackUrl":"http://callback.invalid/notifications"}]}`)
	s.do("create subscription, no callback", "ven-1", "POST", "/subscriptions", js, `{"clientName":"site-1","objectOperations":[{"objects":["EVENT"],"operations":["CREATE"]}]}`)
	s.do("create subscription, the 3.0.1 operation POST", "ven-1", "POST", "/subscriptions", js, `{"clientName":"site-1","objectOperations":[{"objects":["EVENT"],"operations":["POST"],"callbackUrl":"http://callback.invalid/x"}]}`)
	s.do("create subscription, operation READ", "ven-1", "POST", "/subscriptions", js, `{"clientName":"site-1","objectOperations":[{"objects":["EVENT"],"operations":["READ"],"callbackUrl":"http://callback.invalid/x"}]}`)
	s.do("delete subscription", "ven-1", "DELETE", "/subscriptions/{sub}", "", "")

	// Notifiers and topics.
	s.do("notifiers", "ven-1", "GET", "/notifiers", "", "")
	s.do("notifiers, no token", "", "GET", "/notifiers", "", "")
	for _, p := range []string{"/notifiers/mqtt/topics/programs", "/notifiers/mqtt/topics/programs/{prog}", "/notifiers/mqtt/topics/events", "/notifiers/mqtt/topics/programs/{prog}/events", "/notifiers/mqtt/topics/reports", "/notifiers/mqtt/topics/subscriptions", "/notifiers/mqtt/topics/vens", "/notifiers/mqtt/topics/resources"} {
		s.do("topics "+p, "bl", "GET", p, "", "")
	}
	for _, p := range []string{"/notifiers/mqtt/topics/vens/{site1}", "/notifiers/mqtt/topics/vens/{site1}/events", "/notifiers/mqtt/topics/vens/{site1}/programs", "/notifiers/mqtt/topics/vens/{site1}/resources"} {
		s.do("topics "+p, "ven-1", "GET", p, "", "")
	}
	s.do("topics of another VEN, as ven-2", "ven-2", "GET", "/notifiers/mqtt/topics/vens/{site1}/events", "", "")

	// Clean up, and the objects gone.
	s.do("delete resource", "ven-1", "DELETE", "/resources/{res}", "", "")
	s.do("delete BL resource", "bl", "DELETE", "/resources/{blres}", "", "")
	s.do("delete ven", "bl", "DELETE", "/vens/{ven}", "", "")
	s.do("delete ven-3's ven", "bl", "DELETE", "/vens/{selfVen}", "", "")
	s.do("delete event", "bl", "DELETE", "/events/{ev}", "", "")
	s.do("delete event again", "bl", "DELETE", "/events/{ev}", "", "")
	s.do("delete the event without intervals", "bl", "DELETE", "/events/{ev2}", "", "")
	s.do("delete the program with an unknown member", "bl", "DELETE", "/programs/{extra}", "", "")
	s.do("delete program as a VEN", "ven-1", "DELETE", "/programs/{prog}", "", "")
	s.do("delete program", "bl", "DELETE", "/programs/{prog}", "", "")
	s.do("read deleted program", "bl", "GET", "/programs/{prog}", "", "")
}

func (s *scenario) run301() {
	s.do("server info (not in 3.0.1)", "", "GET", "/auth/server", "", "")
	for _, c := range []string{"bl", "ven-1", "ven-2"} {
		s.token(c)
	}
	s.authErrors()

	s.do("programs without a token", "", "GET", "/programs", "", "")
	s.do("programs with a token the VTN never issued", "bad", "GET", "/programs", "", "")
	_, raw := s.do("programs as BL", "bl", "GET", "/programs", "", "")
	s.find("dayAhead", raw, "programName", "day-ahead")
	s.do("programs as ven-1", "ven-1", "GET", "/programs", "", "")
	s.do("programs as ven-1, its target", "ven-1", "GET", "/programs?targetType=GROUP&targetValues=group-a", "", "")
	s.do("programs, 3.1.0 targets query", "ven-1", "GET", "/programs?targets=group-a", "", "")
	s.paging("programs")

	status, raw := s.do("create program", "bl", "POST", "/programs", js, `{"programName":"transcript-{nonce}","programLongName":"Transcript program","retailerName":"retailer","retailerLongName":"Retailer","programType":"PRICING_TARIFF","country":"NL","principalSubdivision":"UT","timeZoneOffset":"PT1H","intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"P1D"},"programDescriptions":[{"URL":"https://example.com/program"}],"bindingEvents":false,"localPrice":true,"payloadDescriptors":[{"objectType":"EVENT_PAYLOAD_DESCRIPTOR","payloadType":"PRICE","units":"KWH","currency":"EUR"}],"targets":[{"type":"GROUP","values":["transcript-group"]}]}`)
	s.capture("prog", status, raw, "id")
	s.do("read program", "bl", "GET", "/programs/{prog}", "", "")
	s.do("update program", "bl", "PUT", "/programs/{prog}", js, `{"programName":"transcript-{nonce}","programLongName":"Changed","targets":[{"type":"GROUP","values":["transcript-group"]}]}`)
	s.do("create program, 3.1.0 targets", "bl", "POST", "/programs", js, `{"programName":"transcript-310-{nonce}","targets":["transcript-group"]}`)
	s.do("create program, name taken", "bl", "POST", "/programs", js, `{"programName":"transcript-{nonce}"}`)
	s.do("create program as a VEN", "ven-1", "POST", "/programs", js, `{"programName":"transcript-ven-{nonce}"}`)
	s.do("create program, malformed JSON", "bl", "POST", "/programs", js, `{"programName":`)
	s.do("create program, no programName", "bl", "POST", "/programs", js, `{}`)
	s.do("read unknown program", "bl", "GET", "/programs/no-such-program", "", "")

	status, raw = s.do("create event", "bl", "POST", "/events", js, `{"programID":"{prog}","eventName":"transcript-event-{nonce}","priority":2,"targets":[{"type":"GROUP","values":["transcript-group"]}],"reportDescriptors":[{"payloadType":"USAGE","readingType":"DIRECT_READ","units":"KWH","targets":[{"type":"RESOURCE_NAME","values":["battery"]}],"aggregate":false,"startInterval":-1,"numIntervals":-1,"historical":true,"frequency":-1,"repeat":1}],"intervalPeriod":{"start":"2030-01-01T00:00:00Z","duration":"PT1H"},"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[0.17]}]}]}`)
	s.capture("ev", status, raw, "id")
	s.do("events of the program", "bl", "GET", "/events?programID={prog}", "", "")
	s.do("read event", "ven-1", "GET", "/events/{ev}", "", "")
	s.do("update event", "bl", "PUT", "/events/{ev}", js, `{"programID":"{prog}","eventName":"transcript-event-{nonce}","intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[0.5]}]}]}`)
	s.paging("events")

	_, raw = s.do("vens as BL", "bl", "GET", "/vens", "", "")
	s.find("site1", raw, "venName", "site-1")
	s.do("vens as ven-1", "ven-1", "GET", "/vens", "", "")
	status, raw = s.do("create ven", "bl", "POST", "/vens", js, `{"objectType":"VEN","venName":"transcript-ven-{nonce}","attributes":[{"type":"LOCATION","values":["NL"]}],"targets":[{"type":"GROUP","values":["transcript-group"]}]}`)
	s.capture("ven", status, raw, "id")
	s.do("read ven", "bl", "GET", "/vens/{ven}", "", "")
	s.do("update ven", "bl", "PUT", "/vens/{ven}", js, `{"objectType":"VEN","venName":"transcript-ven-{nonce}"}`)
	s.do("resources of site-1", "ven-1", "GET", "/vens/{site1}/resources", "", "")
	status, raw = s.do("create resource", "bl", "POST", "/vens/{ven}/resources", js, `{"objectType":"RESOURCE","resourceName":"transcript-res-{nonce}","venID":"{ven}","attributes":[{"type":"MAX_POWER_CONSUMPTION","values":[11]}]}`)
	s.capture("res", status, raw, "id")
	s.do("read resource", "bl", "GET", "/vens/{ven}/resources/{res}", "", "")
	s.do("update resource", "bl", "PUT", "/vens/{ven}/resources/{res}", js, `{"objectType":"RESOURCE","resourceName":"transcript-res-{nonce}","venID":"{ven}"}`)
	s.do("3.1.0 resources path", "bl", "GET", "/resources", "", "")

	status, raw = s.do("ven-1 creates a report", "ven-1", "POST", "/reports", js, `{"programID":"{prog}","eventID":"{ev}","clientName":"site-1","reportName":"transcript-report-{nonce}","resources":[{"resourceName":"battery","intervals":[{"id":0,"payloads":[{"type":"USAGE","values":[1.25]}]}]}]}`)
	s.capture("rep", status, raw, "id")
	s.do("reports of the program", "bl", "GET", "/reports?programID={prog}", "", "")
	s.do("read report", "ven-1", "GET", "/reports/{rep}", "", "")
	s.do("update report", "ven-1", "PUT", "/reports/{rep}", js, `{"programID":"{prog}","eventID":"{ev}","clientName":"site-1","resources":[{"resourceName":"battery","intervals":[{"id":0,"payloads":[{"type":"USAGE","values":[2.5]}]}]}]}`)
	s.do("delete report as the VEN", "ven-1", "DELETE", "/reports/{rep}", "", "")
	s.do("delete report as BL", "bl", "DELETE", "/reports/{rep}", "", "")

	s.do("subscriptions", "ven-1", "GET", "/subscriptions", "", "")
	s.do("create subscription", "ven-1", "POST", "/subscriptions", js, `{"clientName":"site-1","programID":"{prog}","objectOperations":[{"objects":["EVENT"],"operations":["POST"],"callbackUrl":"http://callback.invalid/notifications","bearerToken":"transcript-callback-token"}]}`)
	s.do("notifiers (not in 3.0.1)", "ven-1", "GET", "/notifiers", "", "")

	s.do("delete resource", "bl", "DELETE", "/vens/{ven}/resources/{res}", "", "")
	s.do("delete ven", "bl", "DELETE", "/vens/{ven}", "", "")
	s.do("delete event", "bl", "DELETE", "/events/{ev}", "", "")
	s.do("delete program", "bl", "DELETE", "/programs/{prog}", "", "")
	s.do("read deleted program", "bl", "GET", "/programs/{prog}", "", "")
}
