//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// authority is a certificate authority made for one test, and the
// directory that holds its certificate for a container to trust.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	dir  string
}

func newAuthority(t *testing.T) *authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "go-openadr interop test authority"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	// The container reads it whoever it runs as.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &authority{cert: cert, key: key, dir: dir}
}

// serverConfig returns the TLS configuration of a server with a
// certificate of the authority, for the given host name and for loopback.
func (a *authority) serverConfig(t *testing.T, host string) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "go-openadr VTN"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = append(template.IPAddresses, ip)
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	// As the specification asks: TLS 1.2 at least.
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// client returns an HTTP client that trusts the authority and nothing else.
func (a *authority) client() *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(a.cert)
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
}

// OpenADR requires HTTPS, and the other interoperability tests run over
// plain HTTP on loopback. Here the ElaadNL client reaches this module's
// VTN over TLS, with a certificate of an authority it was given: token
// request and API calls, reads and writes. And without that authority it
// does not get in, which shows that it looked.
func TestElaadNLOverTLS(t *testing.T) {
	ca := newAuthority(t)
	vtn := startGoVTNWith(t, "elaadnl", "baseline", vtnOptions{ca: ca})
	if !strings.HasPrefix(vtn.client.url, "https://") {
		t.Fatalf("the VTN is at %s", vtn.client.url)
	}

	// Reads as a VEN and as Business Logic: the token is fetched from the
	// endpoint the VTN names, which is https as well.
	r := vtn.run(t, "list-programs", "--as", "ven-1")
	if !r.OK || r.status() != 200 || !r.Authenticated || len(r.objects(t)) != 1 {
		t.Fatalf("list-programs as ven-1 over TLS: exit %d, status %d, error %+v", r.exit, r.status(), r.Error)
	}
	if r := vtn.run(t, "list-events", "--as", "bl"); !r.OK || len(r.objects(t)) != 2 {
		t.Errorf("list-events as bl over TLS: exit %d, error %+v", r.exit, r.Error)
	}
	// A write, and what the VTN made of it.
	created := vtn.run(t, "create-program", "--as", "bl", "--body", `{"programName":"over-tls"}`)
	if !created.OK || created.status() != 201 {
		t.Fatalf("create-program over TLS: exit %d, status %d, error %+v", created.exit, created.status(), created.Error)
	}
	if got, err := vtn.store.Programs().Get(ctx(t), created.object(t)["id"].(string)); err != nil || got.Name != "over-tls" {
		t.Errorf("the program in the VTN's store: %+v, %v", got, err)
	}
	// An error is still a problem object, over TLS as without.
	if r := vtn.run(t, "get-program", "--as", "ven-1", "--id", "nope"); r.OK || r.status() != 404 || r.Problem == nil {
		t.Errorf("a program that is not there: exit %d, status %d, problem %v", r.exit, r.status(), r.Problem)
	}

	// The same client without the authority: it does not trust the VTN,
	// sends nothing, and says that it could not connect.
	stranger := vtn.client
	stranger.network = nil
	for i := 0; i < len(vtn.client.network); i++ {
		arg := vtn.client.network[i]
		if (arg == "-v" || arg == "-e") && i+1 < len(vtn.client.network) && strings.Contains(vtn.client.network[i+1], "ca") {
			i++ // the authority's mount and the variables that name it
			continue
		}
		stranger.network = append(stranger.network, arg)
	}
	refused := stranger.run(t, "list-programs", "--as", "ven-1")
	if refused.OK || refused.Authenticated || refused.exit != 3 || refused.Status != nil {
		t.Errorf("without the authority: ok %v, authenticated %v, exit %d, status %v, error %+v; want a refusal to connect",
			refused.OK, refused.Authenticated, refused.exit, refused.Status, refused.Error)
	}
}
