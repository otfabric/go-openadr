//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The reference images of otfabric/openadr-interop v0.1.0, by multi-arch
// index digest. Keep in step with the Makefile and .github/workflows/interop.yml.
var defaultImages = map[string]string{
	"openleadr": "ghcr.io/otfabric/openadr-interop-openleadr@sha256:03131a264f34ac85b65d929702ca6c9ac051093d4996ae3972bf24a706667c7c",
	"elaadnl":   "ghcr.io/otfabric/openadr-interop-elaadnl@sha256:8390bcc0ecd58158c755fc439388a9d7679c65eb3376beaf1ec94711284e91ac",
}

func image(adapter string) string {
	if v := os.Getenv("OPENADR_INTEROP_" + strings.ToUpper(adapter) + "_IMAGE"); v != "" {
		return v
	}
	return defaultImages[adapter]
}

// fixture is the part of an openadr-interop fixture these tests use; see
// docs/FIXTURES.md of that repository.
type fixture struct {
	Name    string `json:"name"`
	Clients []struct {
		Name         string `json:"name"`
		Role         string `json:"role"`
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	} `json:"clients"`
	VENs []struct {
		VENName string   `json:"venName"`
		Client  string   `json:"client"`
		Targets []string `json:"targets"`
	} `json:"vens"`
	Resources []struct {
		ResourceName string `json:"resourceName"`
		VEN          string `json:"ven"`
	} `json:"resources"`
	Programs []json.RawMessage `json:"programs"`
	Events   []json.RawMessage `json:"events"`
}

// credentials returns the client ID and secret of a client of the fixture.
func (f fixture) credentials(t *testing.T, name string) (id, secret string) {
	t.Helper()
	for _, c := range f.Clients {
		if c.Name == name {
			return c.ClientID, c.ClientSecret
		}
	}
	t.Fatalf("the fixture %s has no client %q", f.Name, name)
	return "", ""
}

func docker(ctx context.Context, args ...string) (stdout []byte, exit int, err error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, -1, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, errOut.String())
	}
	return out.Bytes(), 0, nil
}

// needImage returns the image of an adapter, pulling it when it is not
// there. Without Docker or without the image the test is skipped, since
// these tests are opt-in; with OPENADR_INTEROP_REQUIRE set, as in CI, it
// fails instead, so that a run cannot pass by testing nothing.
func needImage(t *testing.T, adapter string) string {
	t.Helper()
	img := image(adapter)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, exit, err := docker(ctx, "image", "inspect", img); err == nil && exit == 0 {
		return img
	}
	if _, exit, err := docker(ctx, "pull", "--quiet", img); err == nil && exit == 0 {
		return img
	}
	reason := fmt.Sprintf("no image %s and it cannot be pulled; set OPENADR_INTEROP_%s_IMAGE to another one, see INTEROP.md",
		img, strings.ToUpper(adapter))
	if os.Getenv("OPENADR_INTEROP_REQUIRE") != "" {
		t.Fatal(reason)
	}
	t.Skip(reason)
	return ""
}

// readFixture asks an image for the fixture baked into it, so that the test
// cannot drift from the image it runs against.
func readFixture(t *testing.T, img, name string) fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, exit, err := docker(ctx, "run", "--rm", img, "print-fixture", name)
	if err != nil || exit != 0 {
		t.Fatalf("print-fixture %s: exit %d, %v", name, exit, err)
	}
	var f fixture
	if err := json.Unmarshal(out, &f); err != nil {
		t.Fatalf("print-fixture %s: %v", name, err)
	}
	return f
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// startServer runs a reference VTN on a loopback port of the host, waits
// until it has loaded its fixture, and returns the URL of its API.
func startServer(t *testing.T, adapter, fixtureName string) string {
	t.Helper()
	img := needImage(t, adapter)
	port := freePort(t)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	out, exit, err := docker(ctx, "run", "-d", "-p", fmt.Sprintf("127.0.0.1:%d:8080", port), img,
		"server", "--fixture", "/fixtures/"+fixtureName+"/fixture.json", "--public-url", url)
	if err != nil || exit != 0 {
		t.Fatalf("starting the %s server: exit %d, %v", adapter, exit, err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", "--tail", "40", id).CombinedOutput()
			t.Logf("%s server log:\n%s", adapter, logs)
		}
		_ = exec.Command("docker", "rm", "-f", id).Run()
	})
	// Readiness is the ready file, not the open port.
	for ctx.Err() == nil {
		if _, exit, _ := docker(ctx, "exec", id, "test", "-f", "/run/openadr-interop/ready"); exit == 0 {
			return url
		}
		if state, _, _ := docker(ctx, "inspect", "-f", "{{.State.Running}}", id); strings.TrimSpace(string(state)) != "true" {
			t.Fatalf("the %s server stopped before it was ready", adapter)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the %s server was not ready in time", adapter)
	return ""
}

// result is the result document of a client operation; see
// docs/CONTAINER_CONTRACT.md of openadr-interop.
type result struct {
	OK    bool `json:"ok"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Authenticated bool            `json:"authenticated"`
	Status        *int            `json:"status"`
	Response      json.RawMessage `json:"response"`
	Problem       map[string]any  `json:"problem"`

	exit int
}

func (r result) errorCode() string {
	if r.Error == nil {
		return ""
	}
	return r.Error.Code
}

func (r result) status() int {
	if r.Status == nil {
		return 0
	}
	return *r.Status
}

// objects decodes the response as a list of objects.
func (r result) objects(t *testing.T) []map[string]any {
	t.Helper()
	var list []map[string]any
	if err := json.Unmarshal(r.Response, &list); err != nil {
		t.Fatalf("the response is not a list of objects: %v: %s", err, r.Response)
	}
	return list
}

// object decodes the response as one object.
func (r result) object(t *testing.T) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(r.Response, &obj); err != nil || obj == nil {
		t.Fatalf("the response is not an object: %v: %s", err, r.Response)
	}
	return obj
}

// clientRunner runs operations of a reference client against a VTN on this
// host.
type clientRunner struct {
	img     string
	fixture string
	// url is the VTN as the container reaches it.
	url string
	// network is how the container reaches the host: see hostAccess.
	network []string
}

// hostAccess says how a container reaches a server on the host's loopback
// interface, and under which host name.
func hostAccess() (host string, dockerArgs []string) {
	if runtime.GOOS == "linux" {
		// The container shares the host's network.
		return "127.0.0.1", []string{"--network", "host"}
	}
	// Docker Desktop forwards this name to the host, loopback included.
	return "host.docker.internal", nil
}

func (c clientRunner) run(t *testing.T, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	full := append([]string{"run", "--rm"}, c.network...)
	full = append(full, c.img, "client")
	full = append(full, args...)
	full = append(full, "--url", c.url, "--fixture", "/fixtures/"+c.fixture+"/fixture.json")
	out, exit, err := docker(ctx, full...)
	if err != nil {
		t.Fatalf("client %s: %v", strings.Join(args, " "), err)
	}
	var r result
	if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil {
		t.Fatalf("client %s: exit %d and no result document: %s", strings.Join(args, " "), exit, out)
	}
	r.exit = exit
	return r
}
