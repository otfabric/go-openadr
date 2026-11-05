// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"crypto/sha1" //nolint:gosec // RFC 6455 defines the handshake with SHA-1; it protects nothing.
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
)

// The tests in this file play a hostile peer: a client that sends the VTN
// what no client should, and a VTN that treats its clients badly. They ask
// that the other side answers with an error of the client's making, never
// a 500, and that it neither hangs nor gives anything away.

// --- a VTN and hostile clients

// Bodies that are too large, too deep, or not an object at all are refused
// as the client's fault, and the VTN goes on serving.
func TestHostileBodies(t *testing.T) {
	vtn := start(t, options{})
	token := vtn.token(t, "bl")
	for name, tc := range map[string]struct {
		body, contentType string
		want              int
	}{
		"one byte over the limit":         {body: `{"programName":"` + strings.Repeat("x", 1<<20) + `"}`, want: http.StatusRequestEntityTooLarge},
		"arrays nested 400000 deep":       {body: strings.Repeat("[", 400000) + strings.Repeat("]", 400000), want: http.StatusBadRequest},
		"objects nested 100000 deep":      {body: strings.Repeat(`{"a":`, 100000) + "1" + strings.Repeat("}", 100000), want: http.StatusBadRequest},
		"null":                            {body: "null", want: http.StatusBadRequest},
		"an array":                        {body: "[]", want: http.StatusBadRequest},
		"a string":                        {body: `"programName"`, want: http.StatusBadRequest},
		"truncated":                       {body: `{"programName":"p`, want: http.StatusBadRequest},
		"two objects":                     {body: `{"programName":"a"}{"programName":"b"}`, want: http.StatusBadRequest},
		"nothing":                         {body: "", want: http.StatusBadRequest},
		"a hundred thousand targets":      {body: `{"programName":"many","targets":["` + strings.Repeat(`t","`, 100000) + `t"]}`},
		"a Content-Type that is not JSON": {body: `{"programName":"typed"}`, contentType: "text/plain; charset=ebcdic"},
	} {
		t.Run(name, func(t *testing.T) {
			header := []string{}
			if tc.contentType != "" {
				header = append(header, "Content-Type", tc.contentType)
			}
			status, _, body := vtn.do(t, "POST", "/programs", token, tc.body, header...)
			switch {
			case tc.want != 0 && status != tc.want:
				t.Errorf("%d %s, want %d", status, body, tc.want)
			case status >= 500:
				t.Errorf("%d %s: the VTN failed on what a client sent", status, body)
			}
		})
	}
	// Still serving, and nothing half-made was kept.
	programs, err := vtn.BL.Programs(context.Background(), client.ProgramQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range programs {
		if p.Name != "many" && p.Name != "typed" {
			t.Errorf("a program %q was made of a refused body", p.Name)
		}
	}
}

// Query parameters at and beyond the edges of what they can hold.
func TestHostileQueries(t *testing.T) {
	vtn := start(t, options{})
	token := vtn.token(t, "bl")
	mustProgram(t, vtn.BL, openadr.Program{Name: "p"})
	many := strings.Repeat("targets=t&", 2000)
	for query, want := range map[string]int{
		"skip=2147483647":                     http.StatusOK,
		"skip=2147483648":                     http.StatusBadRequest,
		"skip=9223372036854775808":            http.StatusBadRequest,
		"skip=-1":                             http.StatusBadRequest,
		"skip=0x10":                           http.StatusBadRequest,
		"skip=1e3":                            http.StatusBadRequest,
		"limit=-1":                            http.StatusBadRequest,
		"limit=2147483648":                    http.StatusBadRequest,
		"limit=0":                             http.StatusOK,
		"skip=0&skip=-1":                      http.StatusOK, // the first counts
		many + "skip=0":                       http.StatusOK,
		"targets=" + strings.Repeat("x", 129): http.StatusBadRequest,
		"targets=":                            http.StatusBadRequest,
		"programID=../../etc":                 http.StatusBadRequest,
		"%zz":                                 http.StatusOK, // not a parameter at all
	} {
		path := "/programs?" + query
		if strings.HasPrefix(query, "programID") {
			path = "/events?" + query
		}
		status, _, body := vtn.do(t, "GET", path, token, "")
		if status != want {
			t.Errorf("%.60s: %d %s, want %d", query, status, body, want)
		}
	}
}

// Many clients delete one object at once: one is told it did, the others
// that it is not there, and none that the VTN failed.
func TestHostileConcurrentDeletes(t *testing.T) {
	vtn := start(t, options{})
	token := vtn.token(t, "bl")
	for round := range 5 {
		p := mustProgram(t, vtn.BL, openadr.Program{Name: fmt.Sprintf("p%d", round)})
		var wg sync.WaitGroup
		var ok, gone, other atomic.Int64
		for range 16 {
			wg.Go(func() {
				req, _ := http.NewRequest("DELETE", vtn.URL+"/programs/"+p.ID, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err := vtn.HTTP.Do(req)
				if err != nil {
					other.Add(1)
					return
				}
				_ = resp.Body.Close()
				switch resp.StatusCode {
				case http.StatusOK:
					ok.Add(1)
				case http.StatusNotFound:
					gone.Add(1)
				default:
					other.Add(1)
				}
			})
		}
		wg.Wait()
		if ok.Load() != 1 || gone.Load() != 15 || other.Load() != 0 {
			t.Errorf("round %d: %d deleted, %d not found, %d else", round, ok.Load(), gone.Load(), other.Load())
		}
	}
}

// Events are created for a program while it is deleted: either the delete
// is refused because events remain, or the events are refused because the
// program is gone. No event outlives its program.
func TestHostileCreateWhileDeleting(t *testing.T) {
	vtn := start(t, options{})
	ctx := context.Background()
	for round := range 10 {
		p := mustProgram(t, vtn.BL, openadr.Program{Name: fmt.Sprintf("p%d", round)})
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				_, err := vtn.BL.CreateEvent(ctx, openadr.Event{ProgramID: p.ID, Name: fmt.Sprintf("e%d", i)})
				if err != nil && !raced(err) {
					t.Errorf("CreateEvent: %v", err)
				}
			})
		}
		wg.Go(func() {
			if _, err := vtn.BL.DeleteProgram(ctx, p.ID); err != nil && !raced(err) {
				t.Errorf("DeleteProgram: %v", err)
			}
		})
		wg.Wait()
	}
	consistent(t, vtn.Store)
}

// --- a hostile VTN and its clients

// hostileSocket is a VTN whose WebSocket floods its client with pings and
// never reads: the client's pongs back up until its writes are stuck.
// written counts what it sent.
func hostileSocket(t *testing.T, written *atomic.Int64) string {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	return newPlainServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")) //nolint:gosec // as above
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
			base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		_ = brw.Flush()
		var pings []byte
		for range 512 {
			pings = append(append(pings, 0x89, 125), strings.Repeat("p", 125)...)
		}
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Write(pings)
			written.Add(int64(n))
			// A write that timed out is a client that does not read
			// either: the flood waits for it.
			if timeout, ok := errors.AsType[net.Error](err); err != nil && (!ok || !timeout.Timeout()) {
				return
			}
		}
	}))
}

// A client whose VTN never reads: the stream still closes, at once, and
// the Next that is waiting returns.
func TestHostileVTNThatDoesNotRead(t *testing.T) {
	var written atomic.Int64
	url := hostileSocket(t, &written)
	ven, err := client.NewVEN(client.Config{BaseURL: url, Tokens: client.StaticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := ven.NotificationStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := make(chan error, 1)
	go func() {
		_, err := stream.Next()
		next <- err
	}()
	// Until the flood stalls: the client answers every ping until its
	// pongs fill the socket buffers, and then it stops reading.
	last, deadline := int64(-1), time.Now().Add(5*time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		now := written.Load()
		if now == last {
			break
		}
		last = now
	}
	closed := make(chan struct{})
	go func() {
		_ = stream.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a VTN that does not read")
	}
	select {
	case err := <-next:
		if err == nil {
			t.Error("Next returned a notification")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next did not return after Close")
	}
}

// A VTN that ignores skip would make Each go on for ever: the repeated
// page ends it with ErrProtocol.
func TestHostileEndlessPages(t *testing.T) {
	var pages atomic.Int64
	page := "[" + strings.Repeat(`{"id":"p1","objectType":"PROGRAM","createdDateTime":"2030-01-01T00:00:00Z","modificationDateTime":"2030-01-01T00:00:00Z","programName":"n"},`, 49) +
		`{"id":"p1","objectType":"PROGRAM","createdDateTime":"2030-01-01T00:00:00Z","modificationDateTime":"2030-01-01T00:00:00Z","programName":"n"}]`
	url := newPlainServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pages.Add(1)
		_, _ = fmt.Fprint(w, page)
	}))
	ven, err := client.NewVEN(client.Config{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A VTN that ignores skip returns the same page again: Each stops at
	// the second one.
	var last error
	n := 0
	for _, err := range client.Each(ctx, func(ctx context.Context, p client.Page) ([]openadr.Program, error) {
		return ven.Programs(ctx, client.ProgramQuery{Page: p})
	}) {
		last = err
		n++
	}
	if !errors.Is(last, openadr.ErrProtocol) || pages.Load() != 2 || n != 51 {
		t.Errorf("Each ended with %v after %d pages and %d items, want ErrProtocol after 2 pages", last, pages.Load(), n)
	}
}

// A VTN whose every page is full of new objects is not told from a long
// result: the caller's context ends Each, with the context's error.
func TestHostileEndlessDistinctPages(t *testing.T) {
	var pages atomic.Int64
	url := newPlainServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := pages.Add(1)
		items := make([]string, 50)
		for i := range items {
			items[i] = fmt.Sprintf(`{"id":"p%d-%d","objectType":"PROGRAM","createdDateTime":"2030-01-01T00:00:00Z","modificationDateTime":"2030-01-01T00:00:00Z","programName":"n"}`, n, i)
		}
		_, _ = fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
	}))
	ven, err := client.NewVEN(client.Config{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var last error
	for _, err := range client.Each(ctx, func(ctx context.Context, p client.Page) ([]openadr.Program, error) {
		return ven.Programs(ctx, client.ProgramQuery{Page: p})
	}) {
		last = err
	}
	if !errors.Is(last, context.DeadlineExceeded) && !errors.Is(last, openadr.ErrTimeout) {
		t.Errorf("Each ended with %v after %d pages, want the context's deadline", last, pages.Load())
	}
}
