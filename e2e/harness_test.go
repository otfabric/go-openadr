// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/internal/mqtttest"
	"github.com/otfabric/go-openadr/notification/mqtt"
	"github.com/otfabric/go-openadr/notification/webhook"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// options select what a test stack is built from. The zero value is a VTN
// with an in-memory store, its own token service and webhooks.
type options struct {
	// store replaces the in-memory store.
	store storage.Store
	// jwt makes the VTN validate JSON Web Tokens of an identity provider
	// that the test plays, in place of its own token service.
	jwt bool
	// mqtt adds a broker and makes the VTN publish to it.
	mqtt bool
	// anonymous makes the VTN answer requests without a token.
	anonymous bool
	// groups makes the VTN serve resource groups.
	groups bool
	// websocket makes the VTN notify over WebSockets.
	websocket *server.WebSocketConfig
	// noWebhooks leaves the webhook sender out.
	noWebhooks bool
	// noCompression makes the VTN answer without gzip.
	noCompression bool
	// sender replaces the settings of the webhook sender; its HTTP client
	// is always the one that trusts the test servers.
	sender *webhook.SenderConfig
}

// stack is a running VTN with clients for one Business Logic client
// ("bl") and two VENs ("ven-1", "ven-2").
type stack struct {
	// URL is the API, under a path prefix, over HTTPS.
	URL string
	// HTTP trusts the certificate of the test servers.
	HTTP  *http.Client
	Store storage.Store
	BL    *client.BL
	VEN1  *client.VEN
	VEN2  *client.VEN
	// Broker is the MQTT broker, with options.mqtt.
	Broker *mqtttest.Broker
	// idp mints tokens, with options.jwt.
	idp *identityProvider
	// VTN is the server itself.
	VTN *server.Server
	// Server is the HTTP server it is mounted on.
	Server *httptest.Server

	issuer *auth.Issuer
}

func start(t testing.TB, o options) *stack {
	t.Helper()
	s := &stack{Store: o.store}
	if s.Store == nil {
		s.Store = storage.NewMemory()
	}
	// Serve under a prefix, as deployments do, and over TLS, as OpenADR
	// requires. The URL is known once the listener is; the VTN is mounted
	// after, because it announces its token endpoint by URL.
	mux := http.NewServeMux()
	ts := httptest.NewUnstartedServer(mux)
	// Tests that cut connections would fill the output with the server's
	// complaints about them.
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.StartTLS()
	t.Cleanup(ts.Close)
	s.URL, s.HTTP, s.Server = ts.URL+"/openadr3", ts.Client(), ts

	cfg := server.Config{
		Store: s.Store, TokenURL: s.URL + "/auth/token",
		AllowAnonymous: o.anonymous, ResourceGroups: o.groups, WebSocket: o.websocket, DisableCompression: o.noCompression,
	}
	if o.jwt {
		s.idp = newIdentityProvider(t)
		cfg.Validator, cfg.TokenURL = s.idp.validator, "https://idp.example/oauth/token"
	} else {
		var err error
		s.issuer, err = auth.NewIssuer(auth.IssuerConfig{Clients: []auth.Client{
			{ID: "bl", Secret: "bl-secret", Scopes: auth.BusinessLogicScopes()},
			{ID: "ven-1", Secret: "ven-1-secret", Scopes: auth.VENScopes()},
			{ID: "ven-2", Secret: "ven-2-secret", Scopes: auth.VENScopes()},
		}})
		if err != nil {
			t.Fatal(err)
		}
		cfg.Validator, cfg.TokenHandler = s.issuer, s.issuer
	}

	// The sender as a deployment has it: https only. It is given the HTTP
	// client that trusts the test certificate, which also means that its
	// own rule against private addresses is not in force here; that rule
	// has tests of its own in package webhook.
	senderConfig := webhook.SenderConfig{Backoff: 20 * time.Millisecond}
	if o.sender != nil {
		senderConfig = *o.sender
	}
	senderConfig.HTTPClient = ts.Client()
	sender := webhook.NewSender(senderConfig)
	if !o.noWebhooks {
		cfg.Webhooks = sender
	}
	shutdown := func(closer func(context.Context) error) {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = closer(ctx)
		})
	}
	shutdown(sender.Close)

	if o.mqtt {
		var err error
		if s.Broker, err = mqtttest.Start(map[string]string{"vtn": "vtn-broker-password", "client": "client-broker-password"}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Broker.Close)
		publisher, err := mqtt.NewPublisher(mqtt.PublisherConfig{Broker: mqtt.Broker{
			URL: s.Broker.URL(), ClientID: "vtn", Username: "vtn", Password: "vtn-broker-password", Backoff: 20 * time.Millisecond,
		}})
		if err != nil {
			t.Fatal(err)
		}
		shutdown(publisher.Close)
		cfg.MQTT = &server.MQTTConfig{Publisher: publisher, URIs: []string{s.Broker.URL()}, TopicPrefix: "oadr/"}
	}

	vtn, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/openadr3/", http.StripPrefix("/openadr3", vtn))
	s.VTN = vtn
	// Before the HTTP server, which waits for the connections the VTN has
	// taken over.
	t.Cleanup(func() { _ = vtn.Close() })

	if s.BL, err = client.NewBL(s.config("bl", "bl-secret")); err != nil {
		t.Fatal(err)
	}
	if s.VEN1, err = client.NewVEN(s.config("ven-1", "ven-1-secret")); err != nil {
		t.Fatal(err)
	}
	if s.VEN2, err = client.NewVEN(s.config("ven-2", "ven-2-secret")); err != nil {
		t.Fatal(err)
	}
	return s
}

// config is the client configuration of one of the stack's OAuth2 clients.
func (s *stack) config(id, secret string) client.Config {
	cfg := client.Config{BaseURL: s.URL, HTTPClient: s.HTTP}
	if s.idp != nil {
		// The identity provider is not the VTN: the client is handed a
		// source of tokens, as it would be with any provider.
		scopes := auth.VENScopes()
		if id == "bl" {
			scopes = auth.BusinessLogicScopes()
		}
		cfg.Tokens = s.idp.source(id, scopes)
		return cfg
	}
	cfg.Credentials = &client.Credentials{ClientID: id, ClientSecret: secret}
	return cfg
}

// token returns a bearer token for one of the stack's clients, for tests
// that speak HTTP themselves.
func (s *stack) token(t testing.TB, id string) string {
	t.Helper()
	if s.idp != nil {
		scopes := auth.VENScopes()
		if id == "bl" {
			scopes = auth.BusinessLogicScopes()
		}
		return s.idp.mint(id, scopes, time.Hour, nil)
	}
	resp, err := s.HTTP.Post(s.URL+"/auth/token", "application/x-www-form-urlencoded",
		strings.NewReader("grant_type=client_credentials&client_id="+id+"&client_secret="+id+"-secret"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		t.Fatalf("no token for %s: %d %v", id, resp.StatusCode, err)
	}
	return body.AccessToken
}

// identityProvider plays an OAuth2 provider that issues EdDSA-signed JSON
// Web Tokens, and holds the validator a VTN would configure for it.
type identityProvider struct {
	key       ed25519.PrivateKey
	validator *auth.JWTValidator
}

const (
	idpIssuer   = "https://idp.example"
	vtnAudience = "https://vtn.example/openadr3"
)

func newIdentityProvider(t testing.TB) *identityProvider {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator(auth.JWTConfig{
		Keys: []auth.JWTKey{{ID: "2026-10", Key: public}}, Issuer: idpIssuer, Audiences: []string{vtnAudience},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &identityProvider{key: private, validator: validator}
}

// mint signs a token. change, when not nil, alters the claims first.
func (p *identityProvider) mint(clientID string, scopes []string, ttl time.Duration, change func(map[string]any)) string {
	claims := map[string]any{
		"iss": idpIssuer, "aud": vtnAudience, "client_id": clientID, "scope": strings.Join(scopes, " "),
		"iat": time.Now().Unix(), "exp": time.Now().Add(ttl).Unix(),
	}
	if change != nil {
		change(claims)
	}
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "2026-10"})
	payload, _ := json.Marshal(claims)
	signed := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	return signed + "." + enc.EncodeToString(ed25519.Sign(p.key, []byte(signed)))
}

// source is a client.TokenSource that mints a token whenever asked, and
// counts how often it was.
func (p *identityProvider) source(clientID string, scopes []string) *tokenSource {
	return &tokenSource{mint: func() string { return p.mint(clientID, scopes, time.Hour, nil) }}
}

type tokenSource struct {
	mint func() string

	mu      sync.Mutex
	current string
	minted  int
}

func (s *tokenSource) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == "" {
		s.current = s.mint()
		s.minted++
	}
	return s.current, nil
}

// Invalidate makes the client's one retry after a 401 use a new token.
func (s *tokenSource) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == token {
		s.current = ""
	}
}

// newPlainServer serves a handler over HTTP, for a test that plays a VTN
// itself, and returns its URL.
func newPlainServer(t testing.TB, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}
