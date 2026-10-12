// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestIdentity(t *testing.T) {
	bl := Identity{ClientID: "bl", Scopes: BusinessLogicScopes()}
	ven := Identity{ClientID: "ven", Scopes: VENScopes()}
	if !bl.IsBusinessLogic() || ven.IsBusinessLogic() {
		t.Error("IsBusinessLogic is wrong")
	}
	for scope, want := range map[string][2]bool{
		ScopeReadAll:        {true, false},
		ScopeReadTargets:    {true, true},
		ScopeReadVENObjects: {true, true},
		ScopeWritePrograms:  {true, false},
		ScopeWriteEvents:    {true, false},
		ScopeWriteReports:   {false, true},
		ScopeWriteVENs:      {true, true},
		"":                  {false, false},
	} {
		if got := [2]bool{bl.Allows(scope), ven.Allows(scope)}; got != want {
			t.Errorf("Allows(%q) for BL, VEN = %v, want %v", scope, got, want)
		}
	}
}

func TestIssuer(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	is, err := NewIssuer(IssuerConfig{
		Clients:  []Client{{ID: "ven", Secret: "s3cret", Scopes: VENScopes()}},
		TokenTTL: time.Minute,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	post := func(form url.Values, basic ...string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/auth/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if len(basic) == 2 {
			req.SetBasicAuth(basic[0], basic[1])
		}
		rec := httptest.NewRecorder()
		is.ServeHTTP(rec, req)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("token endpoint body %q: %v", rec.Body, err)
		}
		return rec.Code, body
	}
	grant := url.Values{"grant_type": {"client_credentials"}}
	with := func(kv ...string) url.Values {
		v := url.Values{"grant_type": {"client_credentials"}}
		for i := 0; i < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}

	for name, tc := range map[string]struct {
		form   url.Values
		basic  []string
		status int
		code   string
	}{
		"no grant type":      {url.Values{"client_id": {"ven"}, "client_secret": {"s3cret"}}, nil, 400, "invalid_request"},
		"other grant type":   {url.Values{"grant_type": {"password"}}, nil, 400, "unsupported_grant_type"},
		"wrong secret":       {with("client_id", "ven", "client_secret", "nope"), nil, 401, "invalid_client"},
		"unknown client":     {with("client_id", "who", "client_secret", ""), nil, 401, "invalid_client"},
		"no credentials":     {grant, nil, 401, "invalid_client"},
		"wrong basic secret": {grant, []string{"ven", "nope"}, 401, "invalid_client"},
		"scope not allowed":  {with("client_id", "ven", "client_secret", "s3cret", "scope", "read_all"), nil, 400, "invalid_scope"},
	} {
		status, body := post(tc.form, tc.basic...)
		if status != tc.status || body["error"] != tc.code || body["status"] != float64(tc.status) {
			t.Errorf("%s: %d %v; want %d %s, also as a problem", name, status, body, tc.status, tc.code)
		}
	}

	status, body := post(with("client_id", "ven", "client_secret", "s3cret"))
	if status != 200 || body["token_type"] != "Bearer" || body["expires_in"] != float64(60) {
		t.Fatalf("token response = %d %v", status, body)
	}
	token := body["access_token"].(string)
	id, err := is.Validate(context.Background(), token)
	if err != nil || id.ClientID != "ven" || !slices.Equal(id.Scopes, VENScopes()) {
		t.Errorf("Validate = %+v, %v", id, err)
	}

	status, body = post(with("scope", "read_targets write_reports"), "ven", "s3cret")
	if status != 200 || body["scope"] != "read_targets write_reports" {
		t.Fatalf("narrowed token = %d %v", status, body)
	}
	narrow, _ := is.Validate(context.Background(), body["access_token"].(string))
	if narrow.Allows(ScopeWriteVENs) || !narrow.Allows(ScopeWriteReports) {
		t.Errorf("narrowed scopes = %v", narrow.Scopes)
	}

	if _, err := is.Validate(context.Background(), "not-a-token"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("unknown token: %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := is.Validate(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired token: %v", err)
	}

	rec := httptest.NewRecorder()
	is.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/token", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", rec.Code)
	}
}

func TestNewIssuerRejectsBadClients(t *testing.T) {
	for _, clients := range [][]Client{
		{{ID: "", Secret: "s"}},
		{{ID: "a", Secret: ""}},
		{{ID: "a", Secret: "s"}, {ID: "a", Secret: "t"}},
	} {
		if _, err := NewIssuer(IssuerConfig{Clients: clients}); err == nil {
			t.Errorf("NewIssuer(%+v) succeeded", clients)
		}
	}
}
