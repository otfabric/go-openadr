// SPDX-License-Identifier: MIT

package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/internal/transcript"
	"github.com/otfabric/go-openadr/internal/wire"
	"github.com/otfabric/go-openadr/internal/wire/codecs"
)

// Replays of recorded VTN answers (testdata/transcripts) through this
// module's clients. For every request of a transcript in which another
// implementation was the VTN, the client makes the call the request stands
// for, against a server that answers what that VTN answered; then:
//
//   - the request the client sends must be the recorded one when the
//     client made it (the interop tests); a request written by hand (the
//     scenarios, whose steps carry a note) the client may not make the same
//     way, or at all: such steps are counted, not failed;
//   - a 2xx answer must be decoded, and what the client returns, encoded
//     again, must equal the recorded body but for the differences
//     transcript.Known classifies;
//   - any other answer must come back as *openadr.Error with the recorded
//     status and the problem the VTN sent.
//
// Token requests are replayed by TestTranscriptTokens.

const corpus = "../testdata/transcripts"

// replayVTN answers with the response of the step it is given.
type replayVTN struct {
	*httptest.Server
	mu   sync.Mutex
	resp *transcript.Response
	seen []seenRequest
	// token answers a token request, for the calls that need one first.
	token func(w http.ResponseWriter, r *http.Request) bool
}

type seenRequest struct {
	method, path, query, auth, contentType string
	form                                   url.Values
	body                                   []byte
}

func newReplayVTN(t *testing.T) *replayVTN {
	v := &replayVTN{}
	v.Server = httptest.NewServer(http.HandlerFunc(v.serve))
	t.Cleanup(v.Close)
	return v
}

// unredact puts the server's own URL where the transcript has the
// placeholder of the recorded VTN's.
func (v *replayVTN) unredact(s string) string {
	return strings.ReplaceAll(s, "http://vtn.invalid", v.URL)
}

func (v *replayVTN) set(resp *transcript.Response) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.resp, v.seen = resp, nil
}

func (v *replayVTN) requests() []seenRequest {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.seen)
}

func (v *replayVTN) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seen = append(v.seen, seenRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), form, body})
	if v.token != nil && v.token(w, r) {
		return
	}
	if v.resp == nil {
		http.Error(w, "no step", http.StatusTeapot)
		return
	}
	for k, val := range v.resp.Headers {
		if k == "Content-Encoding" {
			// The transcript keeps bodies decoded.
			continue
		}
		w.Header().Set(k, v.unredact(val))
	}
	w.WriteHeader(v.resp.Status)
	_, _ = w.Write([]byte(v.unredact(string(v.resp.Bytes()))))
}

// clientsFor builds the clients of a step, with the recorded token or none.
func clientsFor(t *testing.T, v *replayVTN, rev openadr.Version, s transcript.Step) (*client.VEN, *client.BL) {
	t.Helper()
	cfg := client.Config{BaseURL: v.URL, Version: rev}
	if tok, ok := strings.CutPrefix(s.Request.Headers["Authorization"], "Bearer "); ok {
		cfg.Tokens = client.StaticToken(tok)
	}
	ven, err := client.NewVEN(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bl, err := client.NewBL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ven, bl
}

// page parses skip and limit. ok is false for values the client cannot
// send: it sends no skip or limit of zero or less.
func page(q url.Values) (client.Page, bool) {
	var p client.Page
	for name, dst := range map[string]*int{"skip": &p.Skip, "limit": &p.Limit} {
		if s := q.Get(name); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n <= 0 {
				return p, false
			}
			*dst = n
		}
	}
	return p, true
}

func encList[T any](oc wire.ObjectCodec[T]) func([]T, error) ([]byte, error) {
	return func(items []T, err error) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return oc.MarshalList(items)
	}
}

func encOne[T any](oc wire.ObjectCodec[T]) func(T, error) ([]byte, error) {
	return func(item T, err error) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return oc.Marshal(item, wire.Object)
	}
}

// reader is what both clients read.
type reader interface {
	Programs(context.Context, client.ProgramQuery) ([]openadr.Program, error)
	Program(context.Context, string) (openadr.Program, error)
	Events(context.Context, client.EventQuery) ([]openadr.Event, error)
	Event(context.Context, string) (openadr.Event, error)
	Reports(context.Context, client.ReportQuery) ([]openadr.Report, error)
	Report(context.Context, string) (openadr.Report, error)
	VENs(context.Context, client.VENQuery) ([]openadr.VEN, error)
	VEN(context.Context, string) (openadr.VEN, error)
	CreateVEN(context.Context, openadr.VEN) (openadr.VEN, error)
	UpdateVEN(context.Context, openadr.VEN) (openadr.VEN, error)
	DeleteVEN(context.Context, string) (openadr.VEN, error)
	Resources(context.Context, client.ResourceQuery) ([]openadr.Resource, error)
	Resource(context.Context, string) (openadr.Resource, error)
	ResourceOf(context.Context, string, string) (openadr.Resource, error)
	CreateResource(context.Context, openadr.Resource) (openadr.Resource, error)
	UpdateResource(context.Context, openadr.Resource) (openadr.Resource, error)
	DeleteResource(context.Context, string) (openadr.Resource, error)
	DeleteResourceOf(context.Context, string, string) (openadr.Resource, error)
	Subscriptions(context.Context, client.SubscriptionQuery) ([]openadr.Subscription, error)
	Subscription(context.Context, string) (openadr.Subscription, error)
	CreateSubscription(context.Context, openadr.Subscription) (openadr.Subscription, error)
	UpdateSubscription(context.Context, openadr.Subscription) (openadr.Subscription, error)
	DeleteSubscription(context.Context, string) (openadr.Subscription, error)
	Notifiers(context.Context) (openadr.Notifiers, error)
	MQTTTopics(context.Context, client.TopicSet) (openadr.Topics, error)
	ResourceGroups(context.Context, client.ResourceGroupQuery) ([]openadr.ResourceGroup, error)
	ResourceGroup(context.Context, string) (openadr.ResourceGroup, error)
}

var topicSets = map[string]func(string) client.TopicSet{
	"/notifiers/mqtt/topics/programs":                    func(string) client.TopicSet { return client.AllPrograms() },
	"/notifiers/mqtt/topics/events":                      func(string) client.TopicSet { return client.AllEvents() },
	"/notifiers/mqtt/topics/reports":                     func(string) client.TopicSet { return client.AllReports() },
	"/notifiers/mqtt/topics/subscriptions":               func(string) client.TopicSet { return client.AllSubscriptions() },
	"/notifiers/mqtt/topics/vens":                        func(string) client.TopicSet { return client.AllVENs() },
	"/notifiers/mqtt/topics/resources":                   func(string) client.TopicSet { return client.AllResources() },
	"/notifiers/mqtt/topics/programs/{programID}":        client.OneProgram,
	"/notifiers/mqtt/topics/programs/{programID}/events": client.EventsOfProgram,
	"/notifiers/mqtt/topics/vens/{venID}":                client.OneVEN,
	"/notifiers/mqtt/topics/vens/{venID}/events":         client.EventsForVEN,
	"/notifiers/mqtt/topics/vens/{venID}/programs":       client.ProgramsForVEN,
	"/notifiers/mqtt/topics/vens/{venID}/resources":      client.ResourcesOfVEN,
}

type callFunc func(context.Context) ([]byte, error)

// callFor maps a recorded request to the call of this module's client that
// makes it. It returns nil when the client has no such call, or cannot
// express the request's query or body.
func callFor(c *wire.Codec, ven *client.VEN, bl *client.BL, s transcript.Step) callFunc {
	q, _ := url.ParseQuery(s.Request.Query)
	pg, okPage := page(q)
	accepts := func(names ...string) bool {
		for k := range q {
			if !slices.Contains(append(names, "skip", "limit"), k) {
				return false
			}
		}
		return okPage
	}
	parts := strings.Split(strings.Trim(s.Request.Path, "/"), "/")
	seg := func(i int) string {
		if i < len(parts) {
			s, _ := url.PathUnescape(parts[i])
			return s
		}
		return ""
	}
	var r reader = ven
	if s.Actor == transcript.ActorBL {
		r = bl
	}
	body := s.Request.Bytes()
	tg, tt := q["targets"], ""
	if vs, ok := q["targetValues"]; ok {
		tg, tt = vs, q.Get("targetType")
	}
	route := s.Request.Route

	if mk, ok := topicSets[route]; ok && s.Request.Method == http.MethodGet && len(q) == 0 {
		id := ""
		for i, p := range strings.Split(strings.Trim(route, "/"), "/") {
			if strings.HasPrefix(p, "{") {
				id = seg(i)
			}
		}
		set := mk(id)
		return func(ctx context.Context) ([]byte, error) {
			t, err := r.MQTTTopics(ctx, set)
			if err != nil {
				return nil, err
			}
			return c.MarshalTopics(t)
		}
	}

	switch s.Request.Method + " " + route {
	case "GET /notifiers":
		return func(ctx context.Context) ([]byte, error) {
			n, err := r.Notifiers(ctx)
			if err != nil {
				return nil, err
			}
			return c.MarshalNotifiers(n)
		}
	case "GET /programs":
		if !accepts("targets", "targetType", "targetValues") {
			return nil
		}
		pq := client.ProgramQuery{Targets: tg, TargetType: tt, Page: pg}
		return func(ctx context.Context) ([]byte, error) { return encList(c.Programs)(r.Programs(ctx, pq)) }
	case "GET /programs/{programID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.Programs)(r.Program(ctx, seg(1))) }
	case "GET /events":
		if !accepts("targets", "targetType", "targetValues", "programID", "activeOnly") || (q.Has("activeOnly") && q.Get("activeOnly") != "true") {
			return nil
		}
		eq := client.EventQuery{ProgramID: q.Get("programID"), Targets: tg, TargetType: tt, ActiveOnly: q.Get("activeOnly") == "true", Page: pg}
		return func(ctx context.Context) ([]byte, error) { return encList(c.Events)(r.Events(ctx, eq)) }
	case "GET /events/{eventID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.Events)(r.Event(ctx, seg(1))) }
	case "GET /reports":
		if !accepts("programID", "eventID", "clientName") {
			return nil
		}
		rq := client.ReportQuery{ProgramID: q.Get("programID"), EventID: q.Get("eventID"), ClientName: q.Get("clientName"), Page: pg}
		return func(ctx context.Context) ([]byte, error) { return encList(c.Reports)(r.Reports(ctx, rq)) }
	case "GET /reports/{reportID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.Reports)(r.Report(ctx, seg(1))) }
	case "GET /vens":
		if !accepts("venName", "targets", "targetType", "targetValues") {
			return nil
		}
		vq := client.VENQuery{Name: q.Get("venName"), Targets: tg, TargetType: tt, Page: pg}
		return func(ctx context.Context) ([]byte, error) { return encList(c.VENs)(r.VENs(ctx, vq)) }
	case "GET /vens/{venID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.VENs)(r.VEN(ctx, seg(1))) }
	case "DELETE /vens/{venID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.VENs)(r.DeleteVEN(ctx, seg(1))) }
	case "GET /resources":
		if !accepts("resourceName", "venID", "targets", "targetType", "targetValues") {
			return nil
		}
		rq := client.ResourceQuery{Name: q.Get("resourceName"), VENID: q.Get("venID"), Targets: tg, TargetType: tt, Page: pg}
		return func(ctx context.Context) ([]byte, error) { return encList(c.Resources)(r.Resources(ctx, rq)) }
	case "GET /vens/{venID}/resources":
		if !accepts("resourceName", "targets", "targetType", "targetValues") {
			return nil
		}
		rq := client.ResourceQuery{Name: q.Get("resourceName"), VENID: seg(1), Targets: tg, TargetType: tt, Page: pg}
		return func(ctx context.Context) ([]byte, error) { return encList(c.Resources)(r.Resources(ctx, rq)) }
	case "GET /resources/{resourceID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.Resources)(r.Resource(ctx, seg(1))) }
	case "GET /vens/{venID}/resources/{resourceID}":
		return func(ctx context.Context) ([]byte, error) {
			return encOne(c.Resources)(r.ResourceOf(ctx, seg(1), seg(3)))
		}
	case "DELETE /resources/{resourceID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.Resources)(r.DeleteResource(ctx, seg(1))) }
	case "DELETE /vens/{venID}/resources/{resourceID}":
		return func(ctx context.Context) ([]byte, error) {
			return encOne(c.Resources)(r.DeleteResourceOf(ctx, seg(1), seg(3)))
		}
	case "GET /subscriptions":
		if !accepts("programID", "clientName", "objects") {
			return nil
		}
		sq := client.SubscriptionQuery{ProgramID: q.Get("programID"), ClientName: q.Get("clientName"), Page: pg}
		for _, o := range q["objects"] {
			sq.Objects = append(sq.Objects, openadr.ObjectType(o))
		}
		return func(ctx context.Context) ([]byte, error) { return encList(c.Subscriptions)(r.Subscriptions(ctx, sq)) }
	case "GET /subscriptions/{subscriptionID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.Subscriptions)(r.Subscription(ctx, seg(1))) }
	case "DELETE /subscriptions/{subscriptionID}":
		return func(ctx context.Context) ([]byte, error) {
			return encOne(c.Subscriptions)(r.DeleteSubscription(ctx, seg(1)))
		}
	case "GET /resource_groups":
		if !accepts("resourceGroupName", "targets") {
			return nil
		}
		gq := client.ResourceGroupQuery{Name: q.Get("resourceGroupName"), Targets: q["targets"], Page: pg}
		return func(ctx context.Context) ([]byte, error) { return encList(c.ResourceGroups)(r.ResourceGroups(ctx, gq)) }
	case "GET /resource_groups/{resourceGroupID}":
		return func(ctx context.Context) ([]byte, error) {
			return encOne(c.ResourceGroups)(r.ResourceGroup(ctx, seg(1)))
		}
	case "DELETE /programs/{programID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.Programs)(bl.DeleteProgram(ctx, seg(1))) }
	case "DELETE /events/{eventID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.Events)(bl.DeleteEvent(ctx, seg(1))) }
	case "DELETE /reports/{reportID}":
		return func(ctx context.Context) ([]byte, error) { return encOne(c.Reports)(ven.DeleteReport(ctx, seg(1))) }
	case "DELETE /resource_groups/{resourceGroupID}":
		return func(ctx context.Context) ([]byte, error) {
			return encOne(c.ResourceGroups)(bl.DeleteResourceGroup(ctx, seg(1)))
		}
	}
	if len(q) > 0 || (s.Request.Method != http.MethodPost && s.Request.Method != http.MethodPut) {
		return nil
	}
	put := s.Request.Method == http.MethodPut
	id := seg(1)
	switch route {
	case "/programs", "/programs/{programID}":
		return writeCall(c.Programs, body, put, func(p *openadr.Program) { p.ID = id }, bl.CreateProgram, bl.UpdateProgram)
	case "/events", "/events/{eventID}":
		return writeCall(c.Events, body, put, func(e *openadr.Event) { e.ID = id }, bl.CreateEvent, bl.UpdateEvent)
	case "/reports", "/reports/{reportID}":
		return writeCall(c.Reports, body, put, func(rep *openadr.Report) { rep.ID = id }, ven.CreateReport, ven.UpdateReport)
	case "/subscriptions", "/subscriptions/{subscriptionID}":
		return writeCall(c.Subscriptions, body, put, func(sub *openadr.Subscription) { sub.ID = id }, r.CreateSubscription, r.UpdateSubscription)
	case "/resource_groups", "/resource_groups/{resourceGroupID}":
		return writeCall(c.ResourceGroups, body, put, func(g *openadr.ResourceGroup) { g.ID = id }, bl.CreateResourceGroup, bl.UpdateResourceGroup)
	case "/vens", "/vens/{venID}":
		w := r
		if _, form, err := c.VENs.Unmarshal(body); err == nil {
			w = byForm(form, ven, bl, r)
		}
		return writeCall(c.VENs, body, put, func(v *openadr.VEN) { v.ID = id }, w.CreateVEN, w.UpdateVEN)
	case "/resources", "/resources/{resourceID}", "/vens/{venID}/resources", "/vens/{venID}/resources/{resourceID}":
		w := r
		if _, form, err := c.Resources.Unmarshal(body); err == nil {
			w = byForm(form, ven, bl, r)
		}
		if strings.HasPrefix(route, "/vens/") {
			id = seg(3)
		}
		return writeCall(c.Resources, body, put, func(res *openadr.Resource) {
			res.ID = id
			if strings.HasPrefix(route, "/vens/") && res.VENID == "" {
				res.VENID = seg(1)
			}
		}, w.CreateResource, w.UpdateResource)
	}
	return nil
}

func byForm(form wire.Form, ven *client.VEN, bl *client.BL, def reader) reader {
	switch form {
	case wire.BLRequest:
		return bl
	case wire.Request:
		return ven
	case wire.Object:
	}
	return def
}

// writeCall decodes a recorded request body and sends it with create or
// update. It returns nil when the body cannot be decoded: the client
// cannot send it.
func writeCall[T any](oc wire.ObjectCodec[T], body []byte, put bool, setID func(*T), create, update func(context.Context, T) (T, error)) callFunc {
	v, _, err := oc.Unmarshal(body)
	if err != nil {
		return nil
	}
	send := create
	if put {
		setID(&v)
		send = update
	}
	return func(ctx context.Context) ([]byte, error) { return encOne(oc)(send(ctx, v)) }
}

type replayCounts struct {
	steps, replayed, notRepresentable, refusedBeforeSending, requestDiffers, decoded, errorsMatched int
}

// TestTranscriptClientReplay replays every request of every transcript of
// a VTN of another implementation through this module's clients.
func TestTranscriptClientReplay(t *testing.T) {
	files, err := transcript.LoadDir(corpus)
	if err != nil {
		t.Fatal(err)
	}
	var total replayCounts
	for _, f := range files {
		if f.PeerRole != "VTN" {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			n := replayFile(t, f)
			t.Logf("%+v", n)
			total.steps += n.steps
			total.replayed += n.replayed
			total.notRepresentable += n.notRepresentable
			total.refusedBeforeSending += n.refusedBeforeSending
			total.requestDiffers += n.requestDiffers
			total.decoded += n.decoded
			total.errorsMatched += n.errorsMatched
		})
	}
	t.Logf("all VTN transcripts: %+v", total)
	if total.replayed == 0 {
		t.Fatal("nothing replayed")
	}
}

func replayFile(t *testing.T, f transcript.File) replayCounts {
	c, err := codecs.For(f.Revision())
	if err != nil {
		t.Fatal(err)
	}
	v := newReplayVTN(t)
	var n replayCounts
	for _, s := range f.Steps {
		if s.Transport != "http" || s.Origin != transcript.OriginSelf || s.Request.Route == "/auth/token" || s.Request.Route == "/auth/server" {
			continue
		}
		n.steps++
		where := fmt.Sprintf("step %d %s %s?%s (%s)", s.N, s.Request.Method, s.Request.Path, s.Request.Query, s.Note)
		ven, bl := clientsFor(t, v, f.Revision(), s)
		do := callFor(c, ven, bl, s)
		if do == nil {
			t.Logf("%s: the client has no call for it", where)
			n.notRepresentable++
			continue
		}
		v.set(s.Response)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		got, err := do(ctx)
		cancel()
		seen := v.requests()
		if len(seen) == 0 {
			// The client refused before sending: what it refuses must be
			// a request the recording shows to be invalid, or one the
			// revision cannot carry.
			var verr *openadr.ValidationError
			if !errors.As(err, &verr) && !errors.Is(err, errors.ErrUnsupported) {
				t.Errorf("%s: nothing sent, and %v", where, err)
			}
			if s.Note == "" {
				t.Errorf("%s: the client refused a request it made itself when recorded: %v", where, err)
			}
			n.refusedBeforeSending++
			continue
		}
		n.replayed++
		if d := requestDiff(s, seen[0]); d != "" {
			if s.Note == "" {
				t.Errorf("%s: the request differs from the one the client made when recorded: %s", where, d)
			}
			n.requestDiffers++
		}
		checkOutcome(t, f, c, s, where, got, err, &n)
	}
	return n
}

// requestDiff compares what the client sent with the recorded request.
func requestDiff(s transcript.Step, got seenRequest) string {
	want := s.Request
	if got.method != want.Method || got.path != want.Path {
		return fmt.Sprintf("%s %s, recorded %s %s", got.method, got.path, want.Method, want.Path)
	}
	wq, _ := url.ParseQuery(want.Query)
	gq, _ := url.ParseQuery(got.query)
	if fmt.Sprint(wq) != fmt.Sprint(gq) {
		return fmt.Sprintf("query %q, recorded %q", got.query, want.Query)
	}
	if got.auth != want.Headers["Authorization"] {
		return fmt.Sprintf("Authorization %q, recorded %q", got.auth, want.Headers["Authorization"])
	}
	if want.Format == transcript.FormatJSON {
		if diffs := transcript.Compare(want.Bytes(), got.body, transcript.Rules{}); len(diffs) > 0 {
			return fmt.Sprintf("body: %v", diffs)
		}
	}
	return ""
}

func checkOutcome(t *testing.T, f transcript.File, c *wire.Codec, s transcript.Step, where string, got []byte, err error, n *replayCounts) {
	t.Helper()
	resp := s.Response
	if resp.Status/100 == 2 {
		if err != nil {
			if cls, ok := transcript.KnownRefusal(f.Source, s.Request.Route, err); ok {
				t.Logf("%s: refused as classified (%s): %v", where, cls, err)
				return
			}
			t.Errorf("%s: recorded %d, the client returned %v", where, resp.Status, err)
			return
		}
		n.decoded++
		sh, ok := transcript.ResponseShape(s)
		if !ok {
			return
		}
		for _, d := range transcript.Compare(resp.Bytes(), got, transcript.Rules{Equivalent: true}) {
			if _, ok := transcript.Known(f.Source, sh.Kind, d); !ok {
				t.Errorf("%s: unclassified difference %s", where, d)
			}
		}
		return
	}
	var oerr *openadr.Error
	if !errors.As(err, &oerr) {
		t.Errorf("%s: recorded %d, the client returned %v", where, resp.Status, err)
		return
	}
	if oerr.StatusCode != resp.Status {
		t.Errorf("%s: status %d, recorded %d", where, oerr.StatusCode, resp.Status)
	}
	if sh, ok := transcript.ResponseShape(s); ok && sh.Kind == transcript.ShapeProblem {
		var p struct {
			Type, Title, Detail, Instance string
			Status                        int
		}
		_ = json.Unmarshal(resp.JSON, &p)
		want := openadr.Problem{Type: p.Type, Title: p.Title, Status: p.Status, Detail: p.Detail, Instance: p.Instance}
		if oerr.Problem != want {
			t.Errorf("%s: problem %+v, recorded %+v", where, oerr.Problem, want)
		}
	} else if oerr.Problem != (openadr.Problem{}) {
		t.Errorf("%s: a problem %+v from a body that is none", where, oerr.Problem)
	}
	n.errorsMatched++
}
