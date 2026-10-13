// SPDX-License-Identifier: MIT

// Package client holds the two clients of a VTN: VEN, for a virtual end
// node, and BL, for the Business Logic of a utility or aggregator. Both
// speak OpenADR 3.1.0 over HTTP and differ only in the operations they
// offer, which follow the scopes the specification gives each role.
//
// A client is safe for concurrent use. Its methods take a context that
// bounds the whole call, including a token request it may have to make
// first.
//
// Failures reported by the VTN are returned as *openadr.Error and match the
// sentinels of package openadr, such as openadr.ErrNotFound, with errors.Is.
// An object that does not satisfy the schema is rejected before any request
// is sent, with an *openadr.ValidationError.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/wire"
	v310 "github.com/otfabric/go-openadr/internal/wire/v310"
)

// Config configures a client.
type Config struct {
	// BaseURL is the URL of the VTN's API, the one the paths of the
	// specification, such as /programs, are appended to. Required.
	BaseURL string
	// HTTPClient makes the requests; it is where TLS, proxies and timeouts
	// are configured. When nil, a client with a 30 second timeout is used.
	HTTPClient *http.Client
	// Credentials authenticate with the OAuth2 client-credentials flow.
	Credentials *Credentials
	// Tokens supplies bearer tokens some other way. Set at most one of
	// Credentials and Tokens; with neither, requests carry no token, which
	// only a VTN that does not authenticate will accept.
	Tokens TokenSource
	// UserAgent is sent with every request when not empty.
	UserAgent string
	// MaxResponseBytes limits the size of a response body; 16 MiB when zero.
	MaxResponseBytes int64
}

// core is what VEN and BL share: the HTTP exchange with the VTN.
type core struct {
	base    *url.URL
	hc      *http.Client
	tokens  TokenSource
	ua      string
	maxBody int64
	codec   *wire.Codec
}

func newCore(cfg Config) (*core, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("client: Config.BaseURL is required")
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("client: Config.BaseURL: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("client: Config.BaseURL %q is not an absolute http or https URL", cfg.BaseURL)
	}
	if cfg.Credentials != nil && cfg.Tokens != nil {
		return nil, errors.New("client: set Config.Credentials or Config.Tokens, not both")
	}
	// Paths are appended to the base, so it must end in a slash for
	// relative resolution to keep its last segment.
	base.Path = strings.TrimSuffix(base.Path, "/") + "/"
	base.RawQuery, base.Fragment = "", ""

	c := &core{
		base:    base,
		hc:      cfg.HTTPClient,
		tokens:  cfg.Tokens,
		ua:      cfg.UserAgent,
		maxBody: cfg.MaxResponseBytes,
		codec:   v310.Codec(),
	}
	if c.hc == nil {
		c.hc = &http.Client{Timeout: 30 * time.Second}
	}
	if c.maxBody <= 0 {
		c.maxBody = 16 << 20
	}
	if cfg.Credentials != nil {
		src, err := newCredentialSource(*cfg.Credentials, c)
		if err != nil {
			return nil, err
		}
		c.tokens = src
	}
	return c, nil
}

// url returns the URL of an API path such as "programs/abc".
func (c *core) url(path string, query url.Values) string {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	return u.String()
}

// do performs one API call and returns the body of a 2xx response. A 401 is
// retried once with a fresh token: the VTN did not act on the request, and
// the usual cause is a token that expired or was revoked early.
func (c *core) do(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		token := ""
		if c.tokens != nil {
			var err error
			if token, err = c.tokens.Token(ctx); err != nil {
				return nil, fmt.Errorf("openadr: obtaining a token: %w", err)
			}
		}
		respBody, status, err := c.roundTrip(ctx, method, c.url(path, query), token, "application/json", body)
		if err != nil {
			return nil, err
		}
		if status >= 200 && status < 300 {
			return respBody, nil
		}
		if status == http.StatusUnauthorized && attempt == 0 && token != "" {
			if inv, ok := c.tokens.(interface{ Invalidate(token string) }); ok {
				inv.Invalidate(token)
				continue
			}
		}
		apiErr := &openadr.Error{Method: method, Path: "/" + path, StatusCode: status}
		if p, err := c.codec.UnmarshalProblem(respBody); err == nil {
			apiErr.Problem = p
		}
		return nil, apiErr
	}
}

// roundTrip sends one HTTP request and reads the whole response.
func (c *core) roundTrip(ctx context.Context, method, target, token, contentType string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil {
		return nil, 0, fmt.Errorf("openadr: reading the response to %s %s: %w", method, req.URL.Path, err)
	}
	if int64(len(respBody)) > c.maxBody {
		return nil, 0, fmt.Errorf("openadr: the response to %s %s exceeds %d bytes", method, req.URL.Path, c.maxBody)
	}
	return respBody, resp.StatusCode, nil
}

// --- typed operations

func objectPath(collection, id string) (string, error) {
	if !openadr.ValidID(id) {
		return "", &openadr.ValidationError{Field: "id", Reason: fmt.Sprintf("%q is not an object ID", id)}
	}
	return collection + "/" + id, nil
}

func decodeObject[T any](oc wire.ObjectCodec[T], body []byte, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	v, _, err := oc.Unmarshal(body)
	if err != nil {
		return zero, fmt.Errorf("openadr: decoding the response: %w", err)
	}
	return v, nil
}

func getObject[T any](ctx context.Context, c *core, oc wire.ObjectCodec[T], collection, id string) (T, error) {
	path, err := objectPath(collection, id)
	if err != nil {
		var zero T
		return zero, err
	}
	body, err := c.do(ctx, http.MethodGet, path, nil, nil)
	return decodeObject(oc, body, err)
}

func deleteObject[T any](ctx context.Context, c *core, oc wire.ObjectCodec[T], collection, id string) (T, error) {
	path, err := objectPath(collection, id)
	if err != nil {
		var zero T
		return zero, err
	}
	body, err := c.do(ctx, http.MethodDelete, path, nil, nil)
	return decodeObject(oc, body, err)
}

// sendObject creates v, or with an id replaces the object that has it.
func sendObject[T interface{ Validate() error }](ctx context.Context, c *core, oc wire.ObjectCodec[T], collection, id string, v T, form wire.Form) (T, error) {
	var zero T
	method, path := http.MethodPost, collection
	if id != "" {
		var err error
		if path, err = objectPath(collection, id); err != nil {
			return zero, err
		}
		method = http.MethodPut
	}
	if err := v.Validate(); err != nil {
		return zero, err
	}
	reqBody, err := oc.Marshal(v, form)
	if err != nil {
		return zero, err
	}
	body, err := c.do(ctx, method, path, nil, reqBody)
	return decodeObject(oc, body, err)
}

func listObjects[T any](ctx context.Context, c *core, oc wire.ObjectCodec[T], collection string, query url.Values) ([]T, error) {
	body, err := c.do(ctx, http.MethodGet, collection, query, nil)
	if err != nil {
		return nil, err
	}
	vs, err := oc.UnmarshalList(body)
	if err != nil {
		return nil, fmt.Errorf("openadr: decoding the response: %w", err)
	}
	return vs, nil
}

// --- queries

// Page selects a part of a search result. The zero Page asks for the
// beginning of the result in the page size the VTN chooses; a VTN returns
// at most 50 objects per request.
type Page struct {
	// Skip is the number of objects to skip.
	Skip int
	// Limit is the maximum number of objects to return; 0 leaves it to the VTN.
	Limit int
}

type params struct{ url.Values }

func newParams(p Page) params {
	q := params{url.Values{}}
	if p.Skip > 0 {
		q.Set("skip", strconv.Itoa(p.Skip))
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	return q
}

func (q params) str(name, value string) params {
	if value != "" {
		q.Set(name, value)
	}
	return q
}

func (q params) list(name string, values []string) params {
	for _, v := range values {
		q.Add(name, v)
	}
	return q
}

// ProgramQuery filters a search for programs.
type ProgramQuery struct {
	// Targets selects programs that have one of these targets. A VEN has to
	// name the targets it was granted to see the programs that carry them.
	Targets []string
	Page
}

func (q ProgramQuery) values() url.Values {
	return newParams(q.Page).list("targets", q.Targets).Values
}

// EventQuery filters a search for events.
type EventQuery struct {
	ProgramID string
	// Targets works as in ProgramQuery.
	Targets []string
	// ActiveOnly leaves out events that have transpired.
	ActiveOnly bool
	Page
}

func (q EventQuery) values() url.Values {
	p := newParams(q.Page).str("programID", q.ProgramID).list("targets", q.Targets)
	if q.ActiveOnly {
		p.Set("active", "true")
	}
	return p.Values
}

// ReportQuery filters a search for reports.
type ReportQuery struct {
	ProgramID  string
	EventID    string
	ClientName string
	Page
}

func (q ReportQuery) values() url.Values {
	return newParams(q.Page).str("programID", q.ProgramID).str("eventID", q.EventID).str("clientName", q.ClientName).Values
}

// VENQuery filters a search for VENs.
type VENQuery struct {
	Name    string
	Targets []string
	Page
}

func (q VENQuery) values() url.Values {
	return newParams(q.Page).str("venName", q.Name).list("targets", q.Targets).Values
}

// ResourceQuery filters a search for resources.
type ResourceQuery struct {
	Name    string
	VENID   string
	Targets []string
	Page
}

func (q ResourceQuery) values() url.Values {
	return newParams(q.Page).str("resourceName", q.Name).str("venID", q.VENID).list("targets", q.Targets).Values
}

// ResourceGroupQuery filters a search for resource groups.
type ResourceGroupQuery struct {
	Name    string
	Targets []string
	Page
}

func (q ResourceGroupQuery) values() url.Values {
	return newParams(q.Page).str("resourceGroupName", q.Name).list("targets", q.Targets).Values
}

// SubscriptionQuery filters a search for subscriptions.
type SubscriptionQuery struct {
	ProgramID  string
	ClientName string
	// Objects selects subscriptions to any of these kinds of object.
	Objects []openadr.ObjectType
	Page
}

func (q SubscriptionQuery) values() url.Values {
	p := newParams(q.Page).str("programID", q.ProgramID).str("clientName", q.ClientName)
	for _, o := range q.Objects {
		p.Add("objects", string(o))
	}
	return p.Values
}

// TopicSet names a set of objects whose MQTT topics a VTN can be asked for.
// The functions below return the twelve sets OpenADR 3.1.0 defines.
type TopicSet struct {
	path string
	id   string
}

// Topics of every object of a kind. A VTN gives them to Business Logic.

// AllPrograms is every program.
func AllPrograms() TopicSet { return TopicSet{path: "programs"} }

// AllEvents is every event.
func AllEvents() TopicSet { return TopicSet{path: "events"} }

// AllReports is every report.
func AllReports() TopicSet { return TopicSet{path: "reports"} }

// AllSubscriptions is every subscription.
func AllSubscriptions() TopicSet { return TopicSet{path: "subscriptions"} }

// AllVENs is every ven.
func AllVENs() TopicSet { return TopicSet{path: "vens"} }

// AllResources is every resource.
func AllResources() TopicSet { return TopicSet{path: "resources"} }

// OneProgram is the program with the given ID.
func OneProgram(programID string) TopicSet { return TopicSet{path: "programs/%s", id: programID} }

// EventsOfProgram is the events of the program with the given ID.
func EventsOfProgram(programID string) TopicSet {
	return TopicSet{path: "programs/%s/events", id: programID}
}

// Topics of one ven. A VTN gives them to the VEN the ven object belongs to.

// OneVEN is the ven object with the given ID.
func OneVEN(venID string) TopicSet { return TopicSet{path: "vens/%s", id: venID} }

// EventsForVEN is the events the VEN may see.
func EventsForVEN(venID string) TopicSet { return TopicSet{path: "vens/%s/events", id: venID} }

// ProgramsForVEN is the programs the VEN may see.
func ProgramsForVEN(venID string) TopicSet { return TopicSet{path: "vens/%s/programs", id: venID} }

// ResourcesOfVEN is the resources of the ven.
func ResourcesOfVEN(venID string) TopicSet { return TopicSet{path: "vens/%s/resources", id: venID} }

func (c *core) notifiers(ctx context.Context) (openadr.Notifiers, error) {
	body, err := c.do(ctx, http.MethodGet, "notifiers", nil, nil)
	if err != nil {
		return openadr.Notifiers{}, err
	}
	n, err := c.codec.UnmarshalNotifiers(body)
	if err != nil {
		return openadr.Notifiers{}, fmt.Errorf("openadr: decoding the response: %w", err)
	}
	return n, nil
}

func (c *core) topics(ctx context.Context, of TopicSet) (openadr.Topics, error) {
	path := of.path
	if path == "" {
		return openadr.Topics{}, &openadr.ValidationError{Field: "of", Reason: "is the zero TopicSet"}
	}
	if strings.Contains(path, "%s") {
		if !openadr.ValidID(of.id) {
			return openadr.Topics{}, &openadr.ValidationError{Field: "id", Reason: fmt.Sprintf("%q is not an object ID", of.id)}
		}
		path = fmt.Sprintf(path, of.id)
	}
	body, err := c.do(ctx, http.MethodGet, "notifiers/mqtt/topics/"+path, nil, nil)
	if err != nil {
		return openadr.Topics{}, err
	}
	t, err := c.codec.UnmarshalTopics(body)
	if err != nil {
		return openadr.Topics{}, fmt.Errorf("openadr: decoding the response: %w", err)
	}
	return t, nil
}
