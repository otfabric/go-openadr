// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// Client is an OAuth2 client known to an Issuer.
type Client struct {
	ID     string
	Secret string
	// Scopes are the scopes a token for this client may carry.
	Scopes []string
}

// IssuerConfig configures an Issuer.
type IssuerConfig struct {
	Clients []Client
	// TokenTTL is the lifetime of a token; one hour when zero.
	TokenTTL time.Duration
	// Now replaces the clock, for tests.
	Now func() time.Time
}

// Issuer is an in-memory OAuth2 token service for the client-credentials
// grant (RFC 6749 section 4.4). It is the token endpoint as an http.Handler
// and the matching Validator. Tokens are opaque and are lost on restart.
//
// An Issuer is safe for concurrent use.
type Issuer struct {
	clients map[string]Client
	ttl     time.Duration
	now     func() time.Time

	mu     sync.Mutex
	tokens map[string]grant
}

type grant struct {
	identity Identity
	expires  time.Time
}

// NewIssuer returns an Issuer for the given clients.
func NewIssuer(cfg IssuerConfig) (*Issuer, error) {
	is, err := newIssuer(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", openadr.ErrConfiguration, err)
	}
	return is, nil
}

func newIssuer(cfg IssuerConfig) (*Issuer, error) {
	is := &Issuer{
		clients: make(map[string]Client, len(cfg.Clients)),
		ttl:     cfg.TokenTTL,
		now:     cfg.Now,
		tokens:  make(map[string]grant),
	}
	if is.ttl <= 0 {
		is.ttl = time.Hour
	}
	if is.now == nil {
		is.now = time.Now
	}
	for _, c := range cfg.Clients {
		if c.ID == "" || c.Secret == "" {
			return nil, errors.New("auth: a client needs an ID and a secret")
		}
		if _, dup := is.clients[c.ID]; dup {
			return nil, errors.New("auth: duplicate client ID " + c.ID)
		}
		c.Scopes = slices.Clone(c.Scopes)
		is.clients[c.ID] = c
	}
	return is, nil
}

// Validate implements Validator.
func (is *Issuer) Validate(_ context.Context, token string) (Identity, error) {
	is.mu.Lock()
	defer is.mu.Unlock()
	g, ok := is.tokens[token]
	if !ok {
		return Identity{}, ErrInvalidToken
	}
	if !is.now().Before(g.expires) {
		delete(is.tokens, token)
		return Identity{}, ErrInvalidToken
	}
	return Identity{ClientID: g.identity.ClientID, Scopes: slices.Clone(g.identity.Scopes)}, nil
}

// tokenResponse is the clientCredentialResponse schema.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// tokenError carries both the RFC 6749 error members and the members of the
// OpenADR problem object: the OpenAPI document declares the latter for
// /auth/token while its authError schema describes the former.
type tokenError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
	Title            string `json:"title"`
	Status           int    `json:"status"`
	Detail           string `json:"detail,omitempty"`
}

// ServeHTTP is the token endpoint. Client credentials are taken from HTTP
// Basic authentication or from the client_id and client_secret form fields.
func (is *Issuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeTokenError(w, http.StatusMethodNotAllowed, "invalid_request", "the token endpoint accepts POST only")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := r.ParseForm(); err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request", "the request body is not a valid form")
		return
	}
	if gt := r.PostForm.Get("grant_type"); gt != "client_credentials" {
		if gt == "" {
			writeTokenError(w, http.StatusBadRequest, "invalid_request", "grant_type is missing")
		} else {
			writeTokenError(w, http.StatusBadRequest, "unsupported_grant_type", "only client_credentials is supported")
		}
		return
	}
	client, ok := is.authenticate(r)
	if !ok {
		_, _, basic := r.BasicAuth()
		if basic {
			w.Header().Set("WWW-Authenticate", `Basic realm="openadr"`)
		}
		writeTokenError(w, http.StatusUnauthorized, "invalid_client", "unknown client or wrong secret")
		return
	}

	scopes := client.Scopes
	if requested := strings.Fields(r.PostForm.Get("scope")); len(requested) > 0 {
		scopes = nil
		for _, s := range requested {
			if !slices.Contains(client.Scopes, s) {
				description := "a scope that was asked for is not available to this client"
				if scopeToken(s) {
					description = "scope " + s + " is not available to this client"
				}
				writeTokenError(w, http.StatusBadRequest, "invalid_scope", description)
				return
			}
			// RFC 6749 section 3.3: the scope is a set.
			if !slices.Contains(scopes, s) {
				scopes = append(scopes, s)
			}
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeTokenError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	token := hex.EncodeToString(raw)
	now := is.now()

	is.mu.Lock()
	live := 0
	var oldest string
	for t, g := range is.tokens {
		switch {
		case !now.Before(g.expires):
			delete(is.tokens, t)
		case g.identity.ClientID == client.ID:
			live++
			if oldest == "" || g.expires.Before(is.tokens[oldest].expires) {
				oldest = t
			}
		}
	}
	if live >= maxTokensPerClient {
		// A client that keeps asking for tokens does not grow the map
		// without bound: its oldest token makes room.
		delete(is.tokens, oldest)
	}
	is.tokens[token] = grant{identity: Identity{ClientID: client.ID, Scopes: slices.Clone(scopes)}, expires: now.Add(is.ttl)}
	is.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int64(is.ttl / time.Second),
		Scope:       strings.Join(scopes, " "),
	})
}

// scopeToken reports whether s is a scope-token of RFC 6749 section 3.3,
// short enough to repeat. Only such a scope is named in an error: section
// 5.2 limits error_description to the same characters, and a description
// that repeats whatever was sent could be made of anything.
func scopeToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x21 || c == '"' || c == '\\' || c > 0x7e {
			return false
		}
	}
	return true
}

// maxTokensPerClient bounds the tokens of one client that are valid at a
// time; a new one beyond it replaces the oldest.
const maxTokensPerClient = 1024

// authenticate finds the client a token request is from. Credentials are
// taken from HTTP Basic authentication or, without it, from the form.
//
// RFC 6749 section 2.3.1 has the client form-encode its ID and secret
// before Basic authentication encodes them, as golang.org/x/oauth2 does.
// Not every client does: both readings are tried, so that a secret with a
// '+' or a '%' in it works either way.
func (is *Issuer) authenticate(r *http.Request) (Client, bool) {
	id, secret, basic := r.BasicAuth()
	if !basic {
		return is.check(r.PostForm.Get("client_id"), r.PostForm.Get("client_secret"))
	}
	if client, ok := is.check(id, secret); ok {
		return client, true
	}
	// Form decoding changes nothing without a '%' or a '+'.
	if !strings.ContainsAny(id, "%+") && !strings.ContainsAny(secret, "%+") {
		return Client{}, false
	}
	decodedID, errID := url.QueryUnescape(id)
	decodedSecret, errSecret := url.QueryUnescape(secret)
	if errID != nil || errSecret != nil {
		return Client{}, false
	}
	return is.check(decodedID, decodedSecret)
}

// check compares the presented secret with the client's. It compares
// digests, so that the comparison takes the same time whatever the length
// of the presented secret, and runs for unknown clients too.
func (is *Issuer) check(id, secret string) (Client, bool) {
	client, known := is.clients[id]
	want, got := sha256.Sum256([]byte(client.Secret)), sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(want[:], got[:]) != 1 || !known {
		return Client{}, false
	}
	return client, true
}

func writeTokenError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(tokenError{
		Error: code, ErrorDescription: description,
		Title: http.StatusText(status), Status: status, Detail: description,
	})
}
