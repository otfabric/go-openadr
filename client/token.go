// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	openadr "github.com/otfabric/go-openadr"
)

// TokenSource supplies the bearer token for a request. Implementations
// must be safe for concurrent use.
//
// A TokenSource that also has a method Invalidate(token string) is told
// when the VTN rejects a token with 401; the request is then retried once
// with the next token it returns.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a TokenSource that always returns the same token.
type StaticToken string

// Token implements TokenSource.
func (t StaticToken) Token(context.Context) (string, error) { return string(t), nil }

// Credentials are OAuth2 client credentials (RFC 6749 section 4.4).
type Credentials struct {
	ClientID     string
	ClientSecret string
	// Scopes are requested from the token endpoint when not empty.
	Scopes []string
	// TokenURL is the token endpoint. When empty it is discovered from the
	// VTN with GET /auth/server; a client of OpenADR 3.0.1, which has no
	// such operation, uses /auth/token under the VTN's BaseURL.
	TokenURL string
}

// TokenError is a failed request to the OAuth2 token endpoint.
type TokenError struct {
	// StatusCode is the HTTP status of the response.
	StatusCode int
	// Code is the RFC 6749 error code, such as "invalid_client", when the
	// response carried one.
	Code        string
	Description string
}

func (e *TokenError) Error() string {
	msg := fmt.Sprintf("openadr: token endpoint answered %d", e.StatusCode)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return msg
}

// Is matches the sentinel of package openadr for the status: a client the
// token endpoint does not know, or will not serve, is openadr.ErrUnauthorized
// (the status is 400 with invalid_client, or 401, or 403); 429 is
// openadr.ErrTooManyRequests, and a 5xx openadr.ErrServer.
func (e *TokenError) Is(target error) bool {
	switch {
	case e.StatusCode == http.StatusUnauthorized, e.StatusCode == http.StatusForbidden,
		e.StatusCode == http.StatusBadRequest && (e.Code == "invalid_client" || e.Code == "unauthorized_client" || e.Code == "access_denied"):
		return target == openadr.ErrUnauthorized
	case e.StatusCode == http.StatusTooManyRequests:
		return target == openadr.ErrTooManyRequests
	case e.StatusCode >= 500:
		return target == openadr.ErrServer
	}
	return false
}

// credentialSource obtains tokens with the client-credentials flow and
// keeps each one until shortly before it expires.
type credentialSource struct {
	creds Credentials
	core  *core

	// mu serializes token requests: concurrent callers wait for one fetch
	// instead of each starting their own.
	mu       sync.Mutex
	tokenURL string
	token    string
	// expires is zero for a token without a stated lifetime, which is used
	// until the VTN rejects it.
	expires time.Time
}

func newCredentialSource(creds Credentials, c *core) (*credentialSource, error) {
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, errors.New("client: Credentials need a ClientID and a ClientSecret")
	}
	if creds.TokenURL != "" {
		if u, err := url.Parse(creds.TokenURL); err != nil || !u.IsAbs() {
			return nil, fmt.Errorf("client: Credentials.TokenURL %q is not an absolute URL", creds.TokenURL)
		}
	}
	tokenURL := creds.TokenURL
	if tokenURL == "" {
		tokenURL = c.defaultTokenURL()
	}
	return &credentialSource{creds: creds, core: c, tokenURL: tokenURL}, nil
}

// expirySlack is how long before its expiry a token stops being used, so
// that it does not expire on the way to the VTN.
const expirySlack = 10 * time.Second

func (s *credentialSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && (s.expires.IsZero() || time.Now().Before(s.expires)) {
		return s.token, nil
	}
	if s.tokenURL == "" {
		discovered, err := s.discover(ctx)
		if err != nil {
			return "", err
		}
		s.tokenURL = discovered
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.creds.ClientID},
		"client_secret": {s.creds.ClientSecret},
	}
	if len(s.creds.Scopes) > 0 {
		form.Set("scope", strings.Join(s.creds.Scopes, " "))
	}
	body, status, _, err := s.core.roundTrip(ctx, http.MethodPost, s.tokenURL, "", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return "", err
	}
	var resp struct {
		AccessToken      string  `json:"access_token"`
		TokenType        string  `json:"token_type"`
		ExpiresIn        float64 `json:"expires_in"`
		Error            string  `json:"error"`
		ErrorDescription string  `json:"error_description"`
	}
	// The body of an error response is not always JSON; the status decides.
	decodeErr := json.Unmarshal(body, &resp)
	if status != http.StatusOK {
		return "", &TokenError{StatusCode: status, Code: resp.Error, Description: resp.ErrorDescription}
	}
	if decodeErr != nil || resp.AccessToken == "" {
		return "", fmt.Errorf("%w: the token endpoint answered 200 without an access_token", openadr.ErrProtocol)
	}
	if !strings.EqualFold(resp.TokenType, "Bearer") {
		return "", fmt.Errorf("%w: the token endpoint issued a %q token, not a bearer token", openadr.ErrProtocol, resp.TokenType)
	}
	s.token, s.expires = resp.AccessToken, time.Time{}
	if resp.ExpiresIn > 0 {
		s.expires = time.Now().Add(time.Duration(resp.ExpiresIn*float64(time.Second)) - expirySlack)
	}
	return s.token, nil
}

// Invalidate drops the cached token if it is the one the VTN rejected.
func (s *credentialSource) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token {
		s.token = ""
	}
}

// discover asks the VTN for its token endpoint.
func (s *credentialSource) discover(ctx context.Context) (string, error) {
	c := s.core
	body, status, header, err := c.roundTrip(ctx, http.MethodGet, c.url("auth/server", nil), "", "", nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		apiErr := &openadr.Error{Method: http.MethodGet, Path: "/auth/server", StatusCode: status, RetryAfter: parseRetryAfter(header, time.Now())}
		if p, err := c.codec.UnmarshalProblem(body); err == nil {
			apiErr.Problem = p
		}
		return "", fmt.Errorf("openadr: discovering the token endpoint: %w", apiErr)
	}
	tokenURL, err := c.codec.UnmarshalAuthServer(body)
	if err != nil {
		return "", fmt.Errorf("%w: discovering the token endpoint: %w", openadr.ErrProtocol, err)
	}
	// The VTN may name its own endpoint relative to the API.
	ref, err := url.Parse(tokenURL)
	if err != nil {
		return "", fmt.Errorf("openadr: discovering the token endpoint: %w", err)
	}
	resolved := c.base.ResolveReference(ref)
	// The client secret goes to this URL: do not let a VTN reached over
	// TLS send it somewhere in the clear.
	if c.base.Scheme == "https" && resolved.Scheme != "https" {
		return "", fmt.Errorf("%w: discovering the token endpoint: the VTN named %q, which is not https", openadr.ErrProtocol, tokenURL)
	}
	return resolved.String(), nil
}
