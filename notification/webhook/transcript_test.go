// SPDX-License-Identifier: MIT

package webhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/transcript"
	"github.com/otfabric/go-openadr/notification/webhook"
)

// The webhook requests other implementations' VTNs made to this module's
// webhook.Handler (testdata/transcripts), sent to it again: it must answer
// as it did, and decode every notification.
func TestTranscriptWebhooks(t *testing.T) {
	files, err := transcript.LoadDir("../../testdata/transcripts")
	if err != nil {
		t.Fatal(err)
	}
	var requests, notifications int
	for _, f := range files {
		for _, s := range f.Steps {
			if s.Transport != "webhook" {
				continue
			}
			requests++
			token, _ := strings.CutPrefix(s.Request.Headers["Authorization"], "Bearer ")
			var got []openadr.Notification
			h := &webhook.Handler{BearerToken: token, Version: f.Revision(),
				Notify: func(_ context.Context, n openadr.Notification) error { got = append(got, n); return nil }}
			target := s.Request.Path
			if s.Request.Query != "" {
				target += "?" + s.Request.Query
			}
			req := httptest.NewRequest(s.Request.Method, target, bytes.NewReader(s.Request.Bytes()))
			for k, v := range s.Request.Headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != s.Response.Status {
				t.Errorf("%s step %d: status %d, recorded %d", f.Name, s.N, rec.Code, s.Response.Status)
			}
			if s.Request.Method == http.MethodPost && rec.Code/100 == 2 {
				if len(got) != 1 {
					t.Errorf("%s step %d: %d notifications", f.Name, s.N, len(got))
					continue
				}
				notifications++
				var w struct {
					ObjectType string `json:"objectType"`
					Operation  string `json:"operation"`
				}
				_ = json.Unmarshal(s.Request.JSON, &w)
				if string(got[0].ObjectType) != w.ObjectType || string(got[0].Operation) != w.Operation {
					t.Errorf("%s step %d: %s %s, recorded %s %s", f.Name, s.N, got[0].ObjectType, got[0].Operation, w.ObjectType, w.Operation)
				}
			}
		}
	}
	t.Logf("%d webhook requests replayed, %d notifications decoded", requests, notifications)
	if requests == 0 {
		t.Fatal("no webhook requests in the transcripts")
	}
}
