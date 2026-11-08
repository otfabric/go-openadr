// SPDX-License-Identifier: MIT

package transcript

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Manifest is testdata/transcripts/manifest.json: where each file of the
// corpus comes from and what it holds.
type Manifest struct {
	Generated string         `json:"generated"`
	Sources   []Source       `json:"sources"`
	Files     []ManifestFile `json:"files"`
}

// Source is one origin of files.
type Source struct {
	ID string `json:"id"`
	// Level is the evidence level of what the source's peer sent.
	Level    string `json:"level"`
	URL      string `json:"url"`
	Revision string `json:"revision"`
	Fetched  string `json:"fetched"`
	Terms    string `json:"terms"`
	// Peers are the implementations, with versions and images.
	Peers    []string `json:"peers,omitempty"`
	Recorder string   `json:"recorder,omitempty"`
	Privacy  string   `json:"privacy"`
	Missing  string   `json:"missing"`
	Notes    string   `json:"notes,omitempty"`
}

// ManifestFile is one file of the corpus.
type ManifestFile struct {
	File   string `json:"file"`
	Source string `json:"source"`
	// Original is the name and URL of a file taken as it is; empty for a
	// recording made here.
	Original        string         `json:"original,omitempty"`
	URL             string         `json:"url,omitempty"`
	Format          string         `json:"format"`
	Level           string         `json:"level"`
	ProtocolVersion string         `json:"protocolVersion,omitempty"`
	Peer            string         `json:"peer,omitempty"`
	PeerRole        string         `json:"peerRole,omitempty"`
	Size            int64          `json:"size"`
	SHA256          string         `json:"sha256"`
	Steps           int            `json:"steps,omitempty"`
	Operations      map[string]int `json:"operations,omitempty"`
	Statuses        map[string]int `json:"statuses,omitempty"`
}

// LoadManifest reads a manifest.
func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a path of the test corpus
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func fileSum(path string) (int64, string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a path of the test corpus
	if err != nil {
		return 0, "", err
	}
	sum := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(sum[:]), nil
}

// corpusFiles lists the files of the corpus but the manifest, SHA256SUMS
// and README.md, relative to dir.
func corpusFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		switch rel {
		case "manifest.json", "SHA256SUMS", "README.md":
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Verify checks that the manifest lists every file of the corpus with its
// size and SHA256, that every listed file is there, that every file's
// source is described, and that SHA256SUMS agrees.
func (m Manifest) Verify(dir string) error {
	sources := map[string]bool{}
	for _, s := range m.Sources {
		sources[s.ID] = true
	}
	listed := map[string]ManifestFile{}
	for _, f := range m.Files {
		if !sources[f.Source] {
			return fmt.Errorf("%s: source %q is not described", f.File, f.Source)
		}
		listed[f.File] = f
	}
	files, err := corpusFiles(dir)
	if err != nil {
		return err
	}
	for _, name := range files {
		f, ok := listed[name]
		if !ok {
			return fmt.Errorf("%s is not in the manifest", name)
		}
		size, sum, err := fileSum(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if size != f.Size || sum != f.SHA256 {
			return fmt.Errorf("%s: size %d SHA256 %s; the manifest says %d %s", name, size, sum, f.Size, f.SHA256)
		}
		delete(listed, name)
	}
	for name := range listed {
		return fmt.Errorf("%s is in the manifest and not in the corpus", name)
	}
	sums, err := ReadSums(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return err
	}
	if len(sums) != len(m.Files) {
		return fmt.Errorf("SHA256SUMS lists %d files, the manifest %d", len(sums), len(m.Files))
	}
	for _, f := range m.Files {
		if sums[f.File] != f.SHA256 {
			return fmt.Errorf("SHA256SUMS: %s is %q, the manifest says %s", f.File, sums[f.File], f.SHA256)
		}
	}
	return nil
}

// ReadSums reads a SHA256SUMS file (shasum -a 256 format).
func ReadSums(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a path of the test corpus
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			return nil, fmt.Errorf("SHA256SUMS: malformed line %q", line)
		}
		out[name] = sum
	}
	return out, sc.Err()
}

// Rebuild recomputes the file list of a manifest from the corpus: a
// transcript is described by its own header, any other file keeps what the
// manifest said of it. It returns the manifest and the SHA256SUMS text.
func (m Manifest) Rebuild(dir, generated string) (Manifest, []byte, error) {
	old := map[string]ManifestFile{}
	for _, f := range m.Files {
		old[f.File] = f
	}
	files, err := corpusFiles(dir)
	if err != nil {
		return m, nil, err
	}
	m.Generated = generated
	m.Files = nil
	var sums bytes.Buffer
	for _, name := range files {
		size, sum, err := fileSum(filepath.Join(dir, name))
		if err != nil {
			return m, nil, err
		}
		mf := old[name]
		mf.File, mf.Size, mf.SHA256 = name, size, sum
		if strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, "spec/") && !strings.HasPrefix(name, "fixtures/") {
			tr, err := Load(filepath.Join(dir, name))
			if err != nil {
				return m, nil, err
			}
			mf.Source, mf.Format, mf.Level = tr.Source, "transcript", tr.Level
			mf.ProtocolVersion, mf.Peer, mf.PeerRole, mf.Steps = tr.ProtocolVersion, tr.Peer, tr.PeerRole, len(tr.Steps)
			mf.Operations, mf.Statuses = map[string]int{}, map[string]int{}
			for _, s := range tr.Steps {
				switch {
				case s.Request != nil:
					mf.Operations[s.Transport+" "+s.Request.Method+" "+s.Request.Route]++
					mf.Statuses[fmt.Sprint(s.Response.Status)]++
				case s.Message != nil:
					mf.Operations["mqtt publish"]++
				}
			}
		}
		if mf.Source == "" {
			return m, nil, fmt.Errorf("%s: no source in the manifest", name)
		}
		m.Files = append(m.Files, mf)
		fmt.Fprintf(&sums, "%s  %s\n", sum, name)
	}
	return m, sums.Bytes(), nil
}
