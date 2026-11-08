// SPDX-License-Identifier: MIT

package transcript

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Redaction kinds and their placeholders. A placeholder is plain text that
// needs no escaping in JSON, a form or a header, so that replacing a value
// by it changes nothing else of the message.
const (
	// KindSecret is an OAuth2 client secret: redacted-secret-<client>.
	KindSecret = "client-secret"
	// KindToken is an access or refresh token: redacted-token-<n>.
	KindToken = "access-token"
	// KindCallbackToken is the bearerToken of a subscription, which the VTN
	// sends to the callback: redacted-callback-token-<n>.
	KindCallbackToken = "callback-token"
	// KindPassword is a password the VTN hands out (MQTT): redacted-password-<n>.
	KindPassword = "password"
	// KindEndpoint is a URL prefix of the test network (a loopback port):
	// the placeholder is given with Endpoint.
	KindEndpoint = "endpoint"
)

// Redactor replaces credentials and endpoints by deterministic placeholders:
// the same value always by the same placeholder within a transcript, numbered
// in the order of first appearance, so that "the token issued in step 2 is
// the one sent in step 5" survives redaction. Nothing else of a message
// changes.
type Redactor struct {
	mu     sync.Mutex
	values map[string]string // original -> placeholder
	kinds  map[string]string // placeholder -> kind
	counts map[string]int    // placeholder -> replacements
	next   map[string]int    // kind -> last number
	order  []string          // originals, longest first when replacing
}

// NewRedactor returns an empty Redactor.
func NewRedactor() *Redactor {
	return &Redactor{values: map[string]string{}, kinds: map[string]string{}, counts: map[string]int{}, next: map[string]int{}}
}

func (r *Redactor) add(original, placeholder, kind string) string {
	if p, ok := r.values[original]; ok {
		return p
	}
	r.values[original] = placeholder
	r.kinds[placeholder] = kind
	r.order = append(r.order, original)
	sort.SliceStable(r.order, func(i, j int) bool { return len(r.order[i]) > len(r.order[j]) })
	return placeholder
}

func (r *Redactor) numbered(original, kind, prefix string) string {
	if original == "" {
		return ""
	}
	if p, ok := r.values[original]; ok {
		return p
	}
	r.next[kind]++
	return r.add(original, fmt.Sprintf("%s-%d", prefix, r.next[kind]), kind)
}

// Secret registers the client secret of a fixture client.
func (r *Redactor) Secret(client, secret string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.add(secret, "redacted-secret-"+client, KindSecret)
}

// Endpoint registers a URL prefix of the test network and its placeholder,
// such as http://127.0.0.1:61234 for http://vtn.invalid.
func (r *Redactor) Endpoint(original, placeholder string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.add(original, placeholder, KindEndpoint)
}

// Token registers an access token and returns its placeholder.
func (r *Redactor) Token(token string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.numbered(token, KindToken, "redacted-token")
}

// sensitive are JSON members whose string values are credentials.
var sensitive = map[string][2]string{
	"access_token":  {KindToken, "redacted-token"},
	"refresh_token": {KindToken, "redacted-token"},
	"bearerToken":   {KindCallbackToken, "redacted-callback-token"},
	"password":      {KindPassword, "redacted-password"},
	"client_secret": {KindSecret, "redacted-secret"},
}

// scan registers the credentials a JSON document carries.
func (r *Redactor) scan(v any) {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := v[k].(string); ok && s != "" {
				if kind, ok := sensitive[k]; ok && !r.isPlaceholder(s) {
					r.numbered(s, kind[0], kind[1])
				}
			}
			r.scan(v[k])
		}
	case []any:
		for _, e := range v {
			r.scan(e)
		}
	}
}

func (r *Redactor) isPlaceholder(s string) bool {
	_, ok := r.kinds[s]
	return ok
}

// Text redacts a header value or a body. JSON is scanned for credentials
// first; a form for client_secret; an Authorization header for its token or
// its Basic credentials.
func (r *Redactor) Text(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s == "" {
		return s
	}
	if json.Valid([]byte(s)) {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			r.scan(v)
		}
	}
	return r.replace(s)
}

// loopback matches a URL of the test network that was not registered with
// Endpoint, such as the broker a VTN names: its host and ephemeral port
// are replaced by broker.invalid (MQTT) or loopback.invalid.
var loopback = regexp.MustCompile(`\b(mqtts?|https?|wss?)://(?:127\.0\.0\.1|localhost|host\.docker\.internal|\[::1\]):[0-9]+`)

func (r *Redactor) replace(s string) string {
	for _, o := range r.order {
		if n := strings.Count(s, o); n > 0 {
			p := r.values[o]
			r.counts[p] += n
			s = strings.ReplaceAll(s, o, p)
		}
	}
	return loopback.ReplaceAllStringFunc(s, func(m string) string {
		scheme, _, _ := strings.Cut(m, "://")
		host := "loopback.invalid"
		if strings.HasPrefix(scheme, "mqtt") {
			host = "broker.invalid"
		}
		p := scheme + "://" + host
		if _, ok := r.kinds[p]; !ok {
			r.kinds[p] = KindEndpoint
		}
		r.counts[p]++
		return p
	})
}

// Authorization redacts an Authorization header value. A Basic one keeps
// its form: the base64 of the client ID and the placeholder of the secret.
func (r *Redactor) Authorization(h string) string {
	scheme, cred, ok := strings.Cut(h, " ")
	if !ok {
		return r.Text(h)
	}
	switch strings.ToLower(scheme) {
	case "bearer":
		p := r.Token(cred)
		r.mu.Lock()
		r.counts[p]++
		r.mu.Unlock()
		return scheme + " " + p
	case "basic":
		raw, err := base64.StdEncoding.DecodeString(cred)
		if err != nil {
			return scheme + " " + r.Text(cred)
		}
		return scheme + " " + base64.StdEncoding.EncodeToString([]byte(r.Text(string(raw))))
	}
	return r.Text(h)
}

// Form redacts the fields of a form.
func (r *Redactor) Form(form map[string][]string) map[string][]string {
	out := make(map[string][]string, len(form))
	for k, vs := range form {
		red := make([]string, len(vs))
		for i, v := range vs {
			if k == "client_secret" {
				r.mu.Lock()
				if _, known := r.values[v]; !known && !r.isPlaceholder(v) {
					r.numbered(v, KindSecret, "redacted-secret")
				}
				r.mu.Unlock()
			}
			red[i] = r.Text(v)
		}
		out[k] = red
	}
	return out
}

// Summary lists the placeholders used, sorted.
func (r *Redactor) Summary() []Redaction {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Redaction{}
	for p, n := range r.counts {
		if n > 0 {
			out = append(out, Redaction{Kind: r.kinds[p], Placeholder: p, Count: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Placeholder < out[j].Placeholder
	})
	return out
}
