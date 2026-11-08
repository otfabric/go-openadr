// SPDX-License-Identifier: MIT

package transcript

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// OriginHeader marks a request the recording side made itself (OriginSelf)
// when the recorder cannot tell otherwise. The recorder drops it.
const OriginHeader = "X-Openadr-Transcript-Origin"

// NoteHeader carries the Note of a step from the recording scenario. The
// recorder drops it.
const NoteHeader = "X-Openadr-Transcript-Note"

// requestHeaders and responseHeaders are the headers a transcript keeps:
// those with a meaning in OpenADR or HTTP semantics, not those of a
// connection or of a proxy.
var (
	requestHeaders  = []string{"Accept", "Accept-Encoding", "Authorization", "Content-Encoding", "Content-Type", "User-Agent"}
	responseHeaders = []string{"Allow", "Cache-Control", "Content-Encoding", "Content-Type", "Location", "Retry-After", "WWW-Authenticate"}
)

// Recorder builds a transcript from the exchanges it sees. It is safe for
// concurrent use; steps are numbered in the order they complete.
type Recorder struct {
	mu      sync.Mutex
	file    File
	red     *Redactor
	byID    map[string]Client // client ID -> client
	tokens  map[string]string // token placeholder -> client name
	live    map[string]map[string]bool
	defOrig string
	// PeerOrigin says whether a request is the peer's. When nil, a request
	// with OriginHeader is the recording side's and any other the
	// default origin's.
	PeerOrigin func(*http.Request) bool
}

// NewRecorder starts a transcript. clients are those of the fixture, with
// their secrets, which are registered with red and never kept.
// defaultOrigin is the origin of a request that does not say.
func NewRecorder(meta File, clients []Client, secrets map[string]string, red *Redactor, defaultOrigin string) *Recorder {
	rec := &Recorder{file: meta, red: red, byID: map[string]Client{}, tokens: map[string]string{}, live: map[string]map[string]bool{}, defOrig: defaultOrigin}
	rec.file.Schema = Schema
	for _, c := range clients {
		c.SecretPlaceholder = red.Secret(c.Name, secrets[c.Name])
		rec.file.Clients = append(rec.file.Clients, c)
		rec.byID[c.ClientID] = c
	}
	return rec
}

// Handler records what passes through next: requests to a VTN of the
// recording side, made by a peer's client.
func (r *Recorder) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Upgrade") != "" {
			// A WebSocket is not an exchange of one request and one
			// answer: it passes unrecorded.
			next.ServeHTTP(w, req)
			return
		}
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
		origin, note := r.origin(req), req.Header.Get(NoteHeader)
		req.Header.Del(OriginHeader)
		req.Header.Del(NoteHeader)
		cw := httptest.NewRecorder()
		next.ServeHTTP(cw, req)
		for k, vs := range cw.Header() {
			w.Header()[k] = vs
		}
		w.WriteHeader(cw.Code)
		_, _ = w.Write(cw.Body.Bytes())
		r.add("http", origin, note, req, body, cw.Code, cw.Header(), cw.Body.Bytes())
	})
}

// Transport records the exchanges of next: requests of the recording side
// to a peer's VTN, or of a proxy in front of it.
func (r *Recorder) Transport(next http.RoundTripper) http.RoundTripper {
	return roundTripper(func(req *http.Request) (*http.Response, error) {
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
			_ = req.Body.Close()
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		origin, note := r.origin(req), req.Header.Get(NoteHeader)
		req = req.Clone(req.Context())
		req.Header.Del(OriginHeader)
		req.Header.Del(NoteHeader)
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		resp, err := next.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusSwitchingProtocols {
			// A WebSocket: passes unrecorded.
			return resp, nil
		}
		raw, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		if rerr != nil {
			return nil, rerr
		}
		r.add("http", origin, note, req, body, resp.StatusCode, resp.Header, raw)
		return resp, nil
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (r *Recorder) origin(req *http.Request) string {
	if o := req.Header.Get(OriginHeader); o != "" {
		return o
	}
	if r.PeerOrigin != nil {
		if r.PeerOrigin(req) {
			return OriginPeer
		}
		return OriginSelf
	}
	return r.defOrig
}

// Message records an MQTT publication the peer made.
func (r *Recorder) Message(topic string, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	body := r.body("application/json", "", payload)
	r.file.Steps = append(r.file.Steps, Step{N: len(r.file.Steps) + 1, Transport: "mqtt", Actor: ActorVTN, Origin: OriginPeer,
		Message: &Message{Topic: r.red.Text(topic), Body: body}})
}

// Webhook marks the steps recorded so far that were requests to a
// callback URL path as webhook notifications from the VTN.
func (r *Recorder) Webhook(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.file.Steps {
		s := &r.file.Steps[i]
		if s.Request != nil && s.Request.Path == path && s.Transport == "http" {
			s.Transport, s.Actor, s.Client = "webhook", ActorVTN, ""
			s.Request.Route = path
		}
	}
}

// decompress undoes a Content-Encoding the transcript records separately.
func decompress(encoding string, raw []byte) []byte {
	var rd io.Reader
	var err error
	switch strings.ToLower(encoding) {
	case "gzip":
		rd, err = gzip.NewReader(bytes.NewReader(raw))
	case "deflate":
		if rd, err = zlib.NewReader(bytes.NewReader(raw)); err != nil {
			rd, err = flate.NewReader(bytes.NewReader(raw)), nil
		}
	default:
		return raw
	}
	if err != nil {
		return raw
	}
	out, err := io.ReadAll(io.LimitReader(rd, 64<<20))
	if err != nil {
		return raw
	}
	return out
}

func (r *Recorder) body(contentType, encoding string, raw []byte) Body {
	raw = decompress(encoding, raw)
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "x-www-form-urlencoded") {
		if form, err := url.ParseQuery(string(raw)); err == nil {
			red := r.red.Form(form)
			b := NewBody(contentType, []byte(encodeForm(red)))
			b.Encoding = encoding
			return b
		}
	}
	b := NewBody(contentType, []byte(r.red.Text(string(raw))))
	b.Encoding = encoding
	return b
}

func (r *Recorder) headers(h http.Header, keep []string) map[string]string {
	out := map[string]string{}
	for _, k := range keep {
		v := strings.Join(h.Values(k), ", ")
		if v == "" {
			continue
		}
		if k == "Authorization" {
			v = r.red.Authorization(v)
		} else {
			v = r.red.Text(v)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *Recorder) add(transport, origin, note string, req *http.Request, reqBody []byte, status int, h http.Header, respBody []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	step := Step{N: len(r.file.Steps) + 1, Transport: transport, Origin: origin, Note: note}
	q := &Request{Method: req.Method, Path: req.URL.Path, Route: Route(req.URL.Path), Query: r.red.Text(req.URL.RawQuery),
		Headers: r.headers(req.Header, requestHeaders)}
	q.Body = r.body(req.Header.Get("Content-Type"), req.Header.Get("Content-Encoding"), reqBody)
	resp := &Response{Status: status, Headers: r.headers(h, responseHeaders)}
	resp.Body = r.body(h.Get("Content-Type"), h.Get("Content-Encoding"), respBody)
	step.Request, step.Response = q, resp
	step.Actor, step.Client = r.actor(req, q, resp)
	r.track(q, resp)
	step.State = r.state()
	r.file.Steps = append(r.file.Steps, step)
}

// actor finds who sent a request: the client a token was issued to, or for
// a token request, the client asking.
func (r *Recorder) actor(req *http.Request, q *Request, resp *Response) (actor, client string) {
	role := func(c Client) string {
		if strings.EqualFold(c.Role, "bl") {
			return ActorBL
		}
		return ActorVEN
	}
	if q.Route == "/auth/token" {
		id := ""
		if v := q.Form["client_id"]; len(v) > 0 {
			id = v[0]
		} else if u, _, ok := req.BasicAuth(); ok {
			id = u
		}
		if c, ok := r.byID[id]; ok {
			actor, client = role(c), c.Name
			if resp.Format == FormatJSON && resp.Status == http.StatusOK {
				var tok struct {
					AccessToken string `json:"access_token"`
				}
				if json.Unmarshal(resp.JSON, &tok) == nil && tok.AccessToken != "" {
					r.tokens[tok.AccessToken] = c.Name
				}
			}
			return actor, client
		}
		return ActorAnonymous, ""
	}
	auth := q.Headers["Authorization"]
	if p, ok := strings.CutPrefix(auth, "Bearer "); ok {
		if name, ok := r.tokens[p]; ok {
			for _, c := range r.file.Clients {
				if c.Name == name {
					return role(c), c.Name
				}
			}
		}
		return ActorAnonymous, ""
	}
	if strings.HasPrefix(auth, "Basic ") {
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic ")); err == nil {
			id, _, _ := strings.Cut(string(raw), ":")
			if c, ok := r.byID[id]; ok {
				return role(c), c.Name
			}
		}
	}
	return ActorAnonymous, ""
}

// track keeps the IDs of the objects created and not deleted.
func (r *Recorder) track(q *Request, resp *Response) {
	kind := Kind(q.Route)
	if kind == "" || resp.Format != FormatJSON || resp.Status/100 != 2 {
		return
	}
	var obj struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(resp.JSON, &obj) != nil || obj.ID == "" {
		return
	}
	switch q.Method {
	case http.MethodPost, http.MethodPut:
		if r.live[kind] == nil {
			r.live[kind] = map[string]bool{}
		}
		r.live[kind][obj.ID] = true
	case http.MethodDelete:
		delete(r.live[kind], obj.ID)
	}
}

func (r *Recorder) state() map[string][]string {
	out := map[string][]string{}
	for kind, ids := range r.live {
		for id := range ids {
			out[kind] = append(out[kind], id)
		}
		sort.Strings(out[kind])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// File returns the transcript with the redactions made so far.
func (r *Recorder) File() File {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.file
	f.Steps = append([]Step(nil), r.file.Steps...)
	f.Redactions = r.red.Summary()
	return f
}

// Setup records how the recording side's VTN is configured.
func (r *Recorder) Setup(setup map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.file.Setup = setup
}

// Len is the number of steps recorded.
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.file.Steps)
}
