//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/discovery"
)

// Package discovery speaks Multicast DNS with code of its own. These tests
// put it on a network with the implementations a VEN or a VTN will meet
// there: Avahi, which is what Linux has, and OpenLEADR. Multicast does not
// cross from a test process into a container network, so this module's
// side runs in a container too: the tests build interop/mdnsprobe for
// Linux and run it on the same network as the other implementation.

// mdnsNet is a container network of its own for one test, with this
// module's probe built for the containers on it.
type mdnsNet struct {
	t       *testing.T
	network string
	// probe is the directory that holds the mdnsprobe binary.
	probe string
	base  string
}

func newMDNSNet(t *testing.T) *mdnsNet {
	t.Helper()
	base := needImage(t, "alpine")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	arch, exit, err := docker(ctx, "version", "-f", "{{.Server.Arch}}")
	if err != nil || exit != 0 {
		t.Fatalf("asking Docker for its architecture: exit %d, %v", exit, err)
	}
	dir := t.TempDir()
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(dir, "mdnsprobe"), "./mdnsprobe")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+strings.TrimSpace(string(arch)), "CGO_ENABLED=0", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building mdnsprobe: %v\n%s", err, out)
	}
	// The container reads the binary whoever it runs as.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	n := &mdnsNet{t: t, probe: dir, base: base, network: fmt.Sprintf("openadr-mdns-%d", time.Now().UnixNano())}
	if _, exit, err := docker(ctx, "network", "create", n.network); err != nil || exit != 0 {
		t.Fatalf("creating a container network: exit %d, %v", exit, err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", n.network).Run() })
	return n
}

// start runs a container on the network until the test ends and returns
// its ID.
func (n *mdnsNet) start(name string, args ...string) string {
	n.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	full := append([]string{"run", "-d", "--network", n.network, "--hostname", name, "-v", n.probe + ":/probe:ro"}, args...)
	out, exit, err := docker(ctx, full...)
	if err != nil || exit != 0 {
		n.t.Fatalf("starting %s: exit %d, %v: %s", name, exit, err, out)
	}
	id := strings.TrimSpace(string(out))
	n.t.Cleanup(func() {
		if n.t.Failed() {
			logs, _ := exec.Command("docker", "logs", "--tail", "40", id).CombinedOutput()
			n.t.Logf("%s log:\n%s", name, logs)
		}
		_ = exec.Command("docker", "rm", "-f", id).Run()
	})
	return id
}

// exec runs a command in a container and returns what it printed.
func (n *mdnsNet) exec(id string, args ...string) (string, int) {
	n.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, exit, err := docker(ctx, append([]string{"exec", id}, args...)...)
	if err != nil {
		n.t.Fatalf("running %v: %v", args, err)
	}
	return string(out), exit
}

// browse runs this module's Browse in a container on the network.
func (n *mdnsNet) browse(timeout time.Duration) []discovery.VTN {
	n.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Minute)
	defer cancel()
	out, exit, err := docker(ctx, "run", "--rm", "--network", n.network, "-v", n.probe+":/probe:ro", n.base,
		"/probe/mdnsprobe", "browse", "-timeout", timeout.String())
	if err != nil || exit != 0 {
		n.t.Fatalf("browsing: exit %d, %v: %s", exit, err, out)
	}
	var vtns []discovery.VTN
	if err := json.Unmarshal(bytes.TrimSpace(out), &vtns); err != nil {
		n.t.Fatalf("browsing printed %q: %v", out, err)
	}
	return vtns
}

// find browses until a VTN of the given instance name shows up.
func (n *mdnsNet) find(instance string) discovery.VTN {
	n.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		vtns := n.browse(3 * time.Second)
		for _, v := range vtns {
			if v.Instance == instance {
				return v
			}
		}
		if time.Now().After(deadline) {
			n.t.Fatalf("no VTN %q was found; found %+v", instance, vtns)
		}
	}
}

// avahi starts a container with the Avahi daemon and its tools, and
// returns it once the daemon answers.
func (n *mdnsNet) avahi(name string) string {
	n.t.Helper()
	// The daemon needs the system bus. Packages are installed when the
	// container starts: there is no image of Avahi to pin, and the base
	// image is pinned.
	id := n.start(name, n.base, "sh", "-c",
		"apk add --no-cache avahi avahi-tools dbus >/dev/null && mkdir -p /run/dbus && dbus-daemon --system && exec avahi-daemon --no-drop-root --no-rlimits")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, exit := n.exec(id, "avahi-browse", "--version"); exit == 0 {
			if out, exit := n.exec(id, "avahi-resolve", "-n", name+".local"); exit == 0 && strings.Contains(out, name+".local") {
				return id
			}
		}
		if time.Now().After(deadline) {
			n.t.Fatal("the Avahi daemon did not come up")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// This module's Advertise, found and resolved by Avahi; and a service
// Avahi publishes, found by this module's Browse.
func TestMDNSWithAvahi(t *testing.T) {
	n := newMDNSNet(t)
	avahi := n.avahi("avahihost")

	// Announced by this module, as a gateway would.
	n.start("govtn", n.base, "/probe/mdnsprobe", "advertise",
		"-instance", "Go VTN", "-host", "govtn", "-port", "8443", "-base-path", "openadr3/3.1.0", "-programs", "local,tariff")

	// Avahi browses the service type and resolves what it finds. In its
	// parsable form a resolved service is a line that begins with "=":
	//   =;eth0;IPv4;Go\032VTN;_openadr3._tcp;local;govtn.local;172.x.x.x;8443;"txt" "txt"
	var resolved string
	deadline := time.Now().Add(90 * time.Second)
	for resolved == "" {
		out, _ := n.exec(avahi, "avahi-browse", "-rtp", "_openadr3._tcp")
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "=;") && strings.Contains(line, `Go\032VTN`) {
				resolved = line
			}
		}
		if resolved == "" && time.Now().After(deadline) {
			t.Fatalf("Avahi did not find the VTN this module announces:\n%s", out)
		}
	}
	fields := strings.Split(resolved, ";")
	if len(fields) < 10 || fields[4] != "_openadr3._tcp" || fields[5] != "local" || fields[6] != "govtn.local" || fields[8] != "8443" {
		t.Errorf("Avahi resolved it as %q", resolved)
	}
	for _, txt := range []string{
		`"version=3.1.0"`, `"base_path=openadr3/3.1.0"`, `"local_url=https://govtn.local:8443/openadr3/3.1.0"`,
		`"requires_auth=True"`, `"program_names=local,tariff"`,
	} {
		if !strings.Contains(resolved, txt) {
			t.Errorf("Avahi's view of the TXT record lacks %s: %s", txt, resolved)
		}
	}
	// And Avahi resolves the host name this module announced, to the
	// address Avahi found the service at.
	out, exit := n.exec(avahi, "avahi-resolve", "-4", "-n", "govtn.local")
	if exit != 0 || !strings.Contains(out, fields[7]) {
		t.Errorf("Avahi resolves govtn.local to %q (exit %d), the service is at %s", strings.TrimSpace(out), exit, fields[7])
	}
	// The service type itself is found by a browser of all types.
	if out, _ := n.exec(avahi, "avahi-browse", "-atp"); !strings.Contains(out, "_openadr3._tcp") {
		t.Errorf("Avahi's browse of all service types does not list _openadr3._tcp:\n%s", out)
	}

	// The other way: Avahi publishes a VTN, as a gateway that runs Avahi
	// would, and this module finds it.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, exit, err := docker(ctx, "exec", "-d", avahi, "avahi-publish", "-s", "Avahi VTN", "_openadr3._tcp", "9443",
		"version=3.1.0", "base_path=oa3", "requires_auth=False", "program_names=a, b", "openapi_url=https://avahihost.local:9443/openapi.yaml"); err != nil || exit != 0 {
		t.Fatalf("publishing with Avahi: exit %d, %v", exit, err)
	}
	found := n.find("Avahi VTN")
	if found.Host != "avahihost.local" || found.Port != 9443 || found.Version != "3.1.0" || found.BasePath != "oa3" || found.RequiresAuth ||
		len(found.ProgramNames) != 2 || found.ProgramNames[1] != "b" || found.OpenAPIURL == "" {
		t.Errorf("found %+v", found)
	}
	if found.URL() != "https://avahihost.local:9443/oa3" {
		t.Errorf("its URL is %q", found.URL())
	}
	if len(found.Addrs) == 0 || !found.Addrs[0].Is4() && !found.Addrs[0].Is6() {
		t.Errorf("no address of the host was found: %v", found.Addrs)
	}
	// Both are on the network now, and one browse finds both: this
	// module's own announcement among them, from another container.
	deadline = time.Now().Add(60 * time.Second)
	for {
		names := map[string]bool{}
		for _, v := range n.browse(3 * time.Second) {
			names[v.Instance] = true
		}
		if names["Go VTN"] && names["Avahi VTN"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("one browse found %v, want both VTNs", names)
		}
	}
}

// OpenLEADR's VTN announces itself and this module finds it; this module
// announces a VTN and OpenLEADR's client library finds it.
//
// It needs a reference image in which OpenLEADR is built with its mdns
// feature (openadr-interop v0.3.0 or later says so with the feature
// serverMdns), which the pinned one is.
func TestMDNSWithOpenLEADR(t *testing.T) {
	img := needImage(t, "openleadr")
	if !has(t, img, "serverMdns") {
		// The pinned image has it. An older image given by environment
		// variable may not: say so instead of passing by doing nothing.
		if os.Getenv("OPENADR_INTEROP_REQUIRE") != "" && os.Getenv("OPENADR_INTEROP_OPENLEADR_IMAGE") == "" {
			t.Fatalf("%s is built without OpenLEADR's mDNS", img)
		}
		t.Skipf("%s is built without OpenLEADR's mDNS (openadr-interop v0.3.0 or later has it)", img)
	}
	n := newMDNSNet(t)

	// OpenLEADR's VTN, on the network, under a name of its own.
	vtn := n.start("olvtn", img, "server", "--mdns-name", "OpenLEADR VTN", "--public-url", "http://olvtn:8080")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, exit := n.exec(vtn, "test", "-f", "/run/openadr-interop/ready"); exit == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the OpenLEADR VTN was not ready in time")
		}
		time.Sleep(300 * time.Millisecond)
	}
	found := n.find("OpenLEADR VTN")
	t.Logf("OpenLEADR announces %+v", found)
	if found.Host != "olvtn.local" || found.Port != 8080 || len(found.Addrs) == 0 {
		t.Errorf("found %+v", found)
	}
	// What it puts in the TXT record: a version, a base path and a URL.
	// It writes the version as 3.1 and says nothing of authentication,
	// which this module reads as "requires a token".
	if !strings.HasPrefix(found.Version, "3.1") || !found.RequiresAuth || !strings.HasPrefix(found.URL(), "http://olvtn.local") || !strings.Contains(found.URL(), ":8080") {
		t.Errorf("version %q, requires auth %v, URL %q", found.Version, found.RequiresAuth, found.URL())
	}

	// The other way. OpenLEADR's client takes a VTN by the local_url of
	// its TXT record, which this module announces.
	if !has(t, img, "mdnsDiscover") {
		t.Skipf("%s cannot look for VTNs", img)
	}
	n.start("govtn", n.base, "/probe/mdnsprobe", "advertise", "-instance", "Go VTN", "-host", "govtn", "-port", "8443", "-base-path", "openadr3")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	want := "https://govtn.local:8443/openadr3"
	deadline = time.Now().Add(90 * time.Second)
	for {
		out, exit, err := docker(ctx, "run", "--rm", "--network", n.network, img, "discover", "--timeout", "10")
		if err != nil || exit != 0 {
			t.Fatalf("OpenLEADR's discovery: exit %d, %v: %s", exit, err, out)
		}
		var result struct {
			VTNs []struct {
				URL string `json:"url"`
			} `json:"vtns"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
			t.Fatalf("OpenLEADR's discovery printed %q: %v", out, err)
		}
		urls := map[string]bool{}
		for _, v := range result.VTNs {
			urls[strings.TrimSuffix(v.URL, "/")] = true
		}
		// It finds its own VTN too, which shows the browse itself works.
		if urls[want] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("OpenLEADR's client did not find the VTN this module announces at %s; it found %v", want, urls)
		}
	}
}
