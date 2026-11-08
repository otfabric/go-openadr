// SPDX-License-Identifier: MIT

// Package transcript holds recorded OpenADR 3 exchanges (HTTP requests and
// answers, webhook and MQTT notifications) and the tools the tests replay
// them with: the file format, a recorder, the deterministic redaction of
// credentials and endpoints, route templates, and a semantic JSON
// comparison that tells null, absent and a different type apart.
//
// The corpus is testdata/transcripts (README.md there). This package is
// internal: its format is not part of the API of the module.
package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Schema names the version of the file format.
const Schema = "openadr-transcript/1"

// Actors of a step.
const (
	ActorVTN       = "VTN"
	ActorVEN       = "VEN"
	ActorBL        = "BL"
	ActorAnonymous = "anonymous"
)

// Origins of a request: whose code made it.
const (
	// OriginPeer is the implementation the transcript is about, when it is
	// the client (a VEN or Business Logic of another implementation) or a
	// VTN of another implementation that notifies.
	OriginPeer = "peer"
	// OriginSelf is go-openadr, or a request the recording scenario made
	// by hand: the side of the exchange that is not evidence.
	OriginSelf = "self"
)

// File is one transcript: an ordered conversation with one implementation.
type File struct {
	Schema string `json:"schema"`
	// Name is the file name without .json.
	Name string `json:"name"`
	// Source is the id of the source in manifest.json.
	Source string `json:"source"`
	// Level is the evidence level (A to E) of what the peer sent.
	Level string `json:"level"`
	// ProtocolVersion is the OpenADR revision spoken: "3.1.0" or "3.0.1".
	ProtocolVersion string `json:"protocolVersion"`
	// Peer is the implementation whose side of the exchange is evidence,
	// and PeerRole what it was: "VTN", "VEN", "BL" or "VEN+BL".
	Peer     string `json:"peer"`
	PeerRole string `json:"peerRole"`
	// Recorded is the date of the recording, Recorder what recorded it.
	Recorded string `json:"recorded"`
	Recorder string `json:"recorder"`
	// Fixture is the openadr-interop fixture the VTN served.
	Fixture string `json:"fixture,omitempty"`
	// Setup is how the recording side's VTN was configured, for a replay
	// against a VTN of this module: anonymous, resourceGroups, webhooks,
	// mqtt, websocket and tls are "true" or "false"; mqttTopicPrefix,
	// mqttAuthentication and mqttUsername as configured.
	Setup       map[string]string `json:"setup,omitempty"`
	Description string            `json:"description,omitempty"`
	Clients     []Client          `json:"clients,omitempty"`
	// Redactions says what was replaced, by which placeholder, how often.
	Redactions []Redaction `json:"redactions"`
	Steps      []Step      `json:"steps"`
}

// Client is an OAuth2 client of the fixture. Its secret is never kept:
// SecretPlaceholder is what stands for it in the steps.
type Client struct {
	Name              string `json:"name"`
	Role              string `json:"role"`
	ClientID          string `json:"clientId"`
	SecretPlaceholder string `json:"secretPlaceholder"`
}

// Step is one exchange: an HTTP request and its answer, or a notification
// (Transport "webhook" is an HTTP request too; "mqtt" has Message).
type Step struct {
	N         int    `json:"n"`
	Transport string `json:"transport"`
	// Actor is who sent the request (or published the message).
	Actor string `json:"actor"`
	// Client is the fixture client the request was authenticated as.
	Client   string    `json:"client,omitempty"`
	Origin   string    `json:"origin"`
	Request  *Request  `json:"request,omitempty"`
	Response *Response `json:"response,omitempty"`
	Message  *Message  `json:"message,omitempty"`
	// State lists, after the step, the IDs of the objects the transcript
	// has seen created and not deleted, by kind.
	State map[string][]string `json:"state,omitempty"`
	// Note says what the step is for, when the recording scenario knows.
	Note string `json:"note,omitempty"`
}

// Request is an HTTP request.
type Request struct {
	Method string `json:"method"`
	// Route is the path template of the specification (/programs/{programID}).
	Route   string            `json:"route"`
	Path    string            `json:"path"`
	Query   string            `json:"query,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body
}

// Response is an HTTP answer.
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body
}

// Message is an MQTT publication.
type Message struct {
	Topic string `json:"topic"`
	Body
}

// Body formats.
const (
	FormatEmpty = "empty"
	FormatJSON  = "json"
	FormatForm  = "form"
	FormatText  = "text"
)

// Body is the content of a request, answer or message, after redaction.
// JSON is kept token for token (only the whitespace is the file's), forms
// as their decoded fields, anything else as text. Length and SHA256 are
// those of the bytes on the wire after redaction and decompression, so a
// replay can tell whether it saw the same bytes.
type Body struct {
	Format string              `json:"bodyFormat"`
	JSON   json.RawMessage     `json:"body,omitempty"`
	Form   map[string][]string `json:"form,omitempty"`
	Text   string              `json:"bodyText,omitempty"`
	Length int                 `json:"bodyLength"`
	SHA256 string              `json:"bodySHA256,omitempty"`
	// Encoding is the Content-Encoding the body arrived in.
	Encoding string `json:"bodyEncoding,omitempty"`
}

// Redaction is one kind of replaced value.
type Redaction struct {
	Kind        string `json:"kind"`
	Placeholder string `json:"placeholder"`
	Count       int    `json:"count"`
}

// Bytes returns the body as bytes: the JSON compacted, the form encoded,
// or the text.
func (b Body) Bytes() []byte {
	switch b.Format {
	case FormatJSON:
		var buf bytes.Buffer
		if err := json.Compact(&buf, b.JSON); err != nil {
			return b.JSON
		}
		return buf.Bytes()
	case FormatForm:
		return []byte(encodeForm(b.Form))
	case FormatText:
		return []byte(b.Text)
	}
	return nil
}

// NewBody classifies raw bytes by their content type.
func NewBody(contentType string, raw []byte) Body {
	sum := sha256.Sum256(raw)
	b := Body{Length: len(raw)}
	if len(raw) == 0 {
		b.Format = FormatEmpty
		return b
	}
	b.SHA256 = hex.EncodeToString(sum[:])
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "x-www-form-urlencoded"):
		if form, err := decodeForm(string(raw)); err == nil {
			b.Format, b.Form = FormatForm, form
			return b
		}
	case json.Valid(raw):
		b.Format, b.JSON = FormatJSON, append(json.RawMessage(nil), raw...)
		return b
	}
	b.Format, b.Text = FormatText, string(raw)
	return b
}

// Load reads one transcript.
func Load(path string) (File, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a path of the test corpus
	if err != nil {
		return File{}, err
	}
	var f File
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	if f.Schema != Schema {
		return File{}, fmt.Errorf("%s: schema %q, want %q", path, f.Schema, Schema)
	}
	if want := strings.TrimSuffix(filepath.Base(path), ".json"); f.Name != want {
		return File{}, fmt.Errorf("%s: name %q does not match the file", path, f.Name)
	}
	for i, s := range f.Steps {
		if s.N != i+1 {
			return File{}, fmt.Errorf("%s: step %d is numbered %d", path, i+1, s.N)
		}
		if s.Transport == "http" && s.Request != nil && s.Request.Route != Route(s.Request.Path) {
			return File{}, fmt.Errorf("%s: step %d: route %q, the path's is %q", path, s.N, s.Request.Route, Route(s.Request.Path))
		}
	}
	return f, nil
}

// LoadDir reads every transcript (*.json but manifest.json) under dir,
// sorted by path.
func LoadDir(dir string) ([]File, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// spec/ holds the OpenAPI documents and their cases, fixtures/
			// the fixtures the VTNs served: no transcripts.
			if d.Name() == "spec" || d.Name() == "fixtures" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".json") && d.Name() != "manifest.json" {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	files := make([]File, 0, len(paths))
	for _, p := range paths {
		f, err := Load(p)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, errors.New("no transcripts in " + dir)
	}
	return files, nil
}

// Marshal encodes a transcript as the corpus keeps it: indented, without
// HTML escaping, ending in a newline.
func Marshal(f File) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
