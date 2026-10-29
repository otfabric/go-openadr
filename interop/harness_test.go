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
	"slices"
	"strings"
	"testing"
	"time"
)

// The reference images of otfabric/openadr-interop v0.5.0, by multi-arch
// index digest. Keep in step with the Makefile and .github/workflows/interop.yml.
var defaultImages = map[string]string{
	"openleadr": "ghcr.io/otfabric/openadr-interop-openleadr@sha256:4f8eb4b1795bd3f21307346eb8cff0e3860f9c9753e841710de50a5af1353ab5",
	"elaadnl":   "ghcr.io/otfabric/openadr-interop-elaadnl@sha256:3c8d2e56b87740f138d3e083303c8333a28cfe6d386aab1a9a009ae70c33d12f",
	// OpenLEADR v0.1.4, a VTN of OpenADR 3.0.1.
	"openleadr301": "ghcr.io/otfabric/openadr-interop-openleadr301@sha256:a2039fff5b7d3e4a3691883a543bb602b885a07ed8a3a2779fed238627c7e1a5",

	// MQTT brokers, as their makers publish them, for brokers_test.go:
	// Eclipse Mosquitto 2.1.2, NanoMQ 0.25.6, HiveMQ Community Edition
	// 2026.5 and EMQX 6.3.1.
	"mosquitto": "eclipse-mosquitto@sha256:38c0da4f2ef84284d47b3b3eeea1cb3bdeabe81ee10caf0cd5c5ff61ee3ea408",
	"nanomq":    "emqx/nanomq@sha256:4c01fb8421b0c5cdf9ad17a913bcdcdb13c6a25947e17caae2b09a338aa669f5",
	"hivemq":    "hivemq/hivemq-ce@sha256:bac6d4d1d815bffe4bc9a7fca8d06ea12b32d34f84ef2f1851ff4548560ee67b",
	"emqx":      "emqx/emqx@sha256:5ecbf93d04e34aaf4096af074e93b773d0838c8ea668cfabdec7976b272d9606",

	// The base of the containers of mdns_test.go: Alpine Linux, in which
	// Avahi is installed and this module's own probe runs.
	"alpine": "alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8",
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
		VENName string `json:"venName"`
		Client  string `json:"client"`
		// Strings in a fixture of OpenADR 3.1.0, values maps in one of
		// 3.0.1.
		Targets json.RawMessage `json:"targets"`
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

// has reports whether an image declares a feature in its capabilities.
func has(t *testing.T, img, feature string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, exit, err := docker(ctx, "run", "--rm", img, "print-capabilities")
	if err != nil || exit != 0 {
		t.Fatalf("print-capabilities: exit %d, %v", exit, err)
	}
	var caps struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(out, &caps); err != nil {
		t.Fatalf("print-capabilities: %v", err)
	}
	return caps.Features[feature]
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

	run := []string{"run", "-d", "-p", fmt.Sprintf("127.0.0.1:%d:8080", port)}
	serve := []string{"server", "--fixture", "/fixtures/" + fixtureName + "/fixture.json", "--public-url", url}
	if has(t, img, "serverMqtt") {
		// The broker is announced by the VTN under the URL it uses itself,
		// so it listens on the same port inside the container and out.
		mqttPort := freePort(t)
		run = append(run, "-p", fmt.Sprintf("127.0.0.1:%d:%d", mqttPort, mqttPort))
		serve = append(serve, "--mqtt-port", fmt.Sprint(mqttPort), "--mqtt-public-url", fmt.Sprintf("mqtt://127.0.0.1:%d", mqttPort))
	}
	out, exit, err := docker(ctx, append(append(run, img), serve...)...)
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
	r, err := c.try(args...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// try is run for a goroutine other than the test's: it reports instead of
// failing the test.
func (c clientRunner) try(args ...string) (result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	full := append([]string{"run", "--rm"}, c.network...)
	full = append(full, c.img, "client")
	full = append(full, args...)
	full = append(full, "--url", c.url, "--fixture", "/fixtures/"+c.fixture+"/fixture.json")
	out, exit, err := docker(ctx, full...)
	if err != nil {
		return result{}, fmt.Errorf("client %s: %w", strings.Join(args, " "), err)
	}
	var r result
	if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil {
		return result{}, fmt.Errorf("client %s: exit %d and no result document: %s", strings.Join(args, " "), exit, out)
	}
	r.exit = exit
	return r, nil
}

// offers reports whether the client of an image has an operation for a
// role, by its capabilities.
func offers(t *testing.T, img, role, operation string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, exit, err := docker(ctx, "run", "--rm", img, "print-capabilities")
	if err != nil || exit != 0 {
		t.Fatalf("print-capabilities: exit %d, %v", exit, err)
	}
	var caps struct {
		ClientOperations map[string][]string `json:"clientOperations"`
	}
	if err := json.Unmarshal(out, &caps); err != nil {
		t.Fatalf("print-capabilities: %v", err)
	}
	return slices.Contains(caps.ClientOperations[role], operation)
}

// needOperation skips a test of an operation the reference client of the
// image does not have: an image of openadr-interop before v0.4.0, given by
// environment variable.
func needOperation(t *testing.T, adapter, role, operation string) {
	t.Helper()
	if img := needImage(t, adapter); !offers(t, img, role, operation) {
		// The pinned image has it. An older image given by environment
		// variable may not: say so instead of passing by doing nothing.
		if os.Getenv("OPENADR_INTEROP_REQUIRE") != "" && os.Getenv("OPENADR_INTEROP_"+strings.ToUpper(adapter)+"_IMAGE") == "" {
			t.Fatalf("%s has no %s for a %s client", img, operation, role)
		}
		t.Skipf("%s has no %s for a %s client (openadr-interop v0.4.0 or later has)", img, operation, role)
	}
}
