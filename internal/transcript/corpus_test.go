// SPDX-License-Identifier: MIT

package transcript_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/internal/transcript"
	"github.com/otfabric/go-openadr/internal/wire/codecs"
)

const corpus = "../../testdata/transcripts"

func loadCorpus(t *testing.T) []transcript.File {
	t.Helper()
	files, err := transcript.LoadDir(corpus)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func pattern(d transcript.Diff) string { return d.Kind + " " + transcript.PathPattern(d.Path) }

// decodeCase is one body decoded and encoded again.
type decodeCase struct {
	file, where string
	shape       transcript.Shape
	body        []byte
	peer        bool
}

func bodies(f transcript.File) []decodeCase {
	var out []decodeCase
	for _, s := range f.Steps {
		where := fmt.Sprintf("%s step %d (%s)", f.Name, s.N, s.Note)
		if sh, ok := transcript.RequestShape(s); ok && s.Request.Format == transcript.FormatJSON && (s.Response == nil || s.Response.Status/100 == 2 || s.Transport == "webhook") {
			out = append(out, decodeCase{f.Name, where + " request " + s.Request.Method + " " + s.Request.Route, sh, s.Request.Bytes(), s.Origin == transcript.OriginPeer})
		}
		if sh, ok := transcript.ResponseShape(s); ok {
			out = append(out, decodeCase{f.Name, where + " response " + s.Request.Method + " " + s.Request.Route, sh, s.Response.Bytes(), s.Origin != transcript.OriginPeer})
		}
		if s.Message != nil && s.Message.Format == transcript.FormatJSON {
			out = append(out, decodeCase{f.Name, where + " mqtt " + s.Message.Topic, transcript.Shape{Kind: transcript.ShapeNotification}, s.Message.Bytes(), true})
		}
	}
	return out
}

// TestCorpusDecode decodes every JSON body of every transcript with the
// codec of its revision, encodes what was decoded again, and compares the
// two. Every difference must be one of knownDecode, classified there.
func TestCorpusDecode(t *testing.T) {
	seen := map[string]map[string]int{} // source -> pattern -> count
	var decoded, peerBodies, failed int
	for _, f := range loadCorpus(t) {
		c, err := codecs.For(f.Revision())
		if err != nil {
			t.Fatal(err)
		}
		for _, dc := range bodies(f) {
			out, err := transcript.Reencode(c, dc.shape, dc.body)
			if err != nil {
				key := f.Source + " " + dc.shape.String() + " decode-error"
				if _, ok := transcript.KnownRefusal(f.Source, dc.shape.String(), err); !ok {
					t.Errorf("%s: decoding %s: %v\n%s", dc.where, dc.shape, err, dc.body)
				}
				failed++
				if seen[f.Source] == nil {
					seen[f.Source] = map[string]int{}
				}
				seen[f.Source][key]++
				continue
			}
			decoded++
			if dc.peer {
				peerBodies++
			}
			for _, d := range transcript.Compare(dc.body, out, transcript.Rules{Equivalent: true}) {
				p := dc.shape.Kind + " " + pattern(d)
				if seen[f.Source] == nil {
					seen[f.Source] = map[string]int{}
				}
				seen[f.Source][p]++
				if _, ok := transcript.Known(f.Source, dc.shape.Kind, d); !ok {
					t.Errorf("%s: %s: unclassified difference %s", dc.where, dc.shape, d)
				}
			}
		}
	}
	t.Logf("%d bodies decoded and encoded again (%d sent by the peer), %d refused as the table says", decoded, peerBodies, failed)
	if os.Getenv("OPENADR_TRANSCRIPT_REPORT") != "" {
		var lines []string
		for src, ps := range seen {
			for p, n := range ps {
				lines = append(lines, fmt.Sprintf("%s %s x%d", src, p, n))
			}
		}
		sort.Strings(lines)
		t.Log("\n" + strings.Join(lines, "\n"))
	}
}

// TestCorpusManifest checks the manifest against the files: every file is
// listed with its SHA256, and every listed file is there.
func TestCorpusManifest(t *testing.T) {
	m, err := transcript.LoadManifest(filepath.Join(corpus, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(corpus); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateManifest rewrites manifest.json and SHA256SUMS from the files
// of the corpus (make transcripts-manifest). It runs only with
// OPENADR_TRANSCRIPT_GENERATE set.
func TestGenerateManifest(t *testing.T) {
	if os.Getenv("OPENADR_TRANSCRIPT_GENERATE") == "" {
		t.Skip("set OPENADR_TRANSCRIPT_GENERATE to rewrite the manifest")
	}
	path := filepath.Join(corpus, "manifest.json")
	m, err := transcript.LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	m, sums, err := m.Rebuild(corpus, time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corpus, "SHA256SUMS"), sums, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestKnownTableUsed decodes the corpus and the cases of the specification
// again and fails on an entry of the tables of known differences that
// matches none of their differences.
func TestKnownTableUsed(t *testing.T) {
	TestCorpusDecode(t)
	TestSpecCases(t)
	if unused := transcript.UnusedKnown(); len(unused) > 0 {
		t.Errorf("entries that match no difference:\n%s", strings.Join(unused, "\n"))
	}
}
