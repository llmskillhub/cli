package main

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"

	skill "github.com/llmskillhub/cli/skillpkg"
)

func testArchive(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "demo/SKILL.md", Method: zip.Deflate}
	hdr.SetMode(0o644)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(body))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const demoSkill = `---
name: demo
description: >
  Cleans and normalises messy CSV files so they import without manual repair. Use this skill
  whenever the user mentions "clean this CSV" or "fix the headers", even if they only describe
  the symptom, such as a spreadsheet that will not import.
---

# Demo

`

// TestInstallRefusesASubstitutedArchive is the reason install exists as a
// command rather than a curl and an unzip.
//
// The bytes arrive from an object store, over a link the API minted. HTTPS says
// they were not altered in transit; it says nothing about whether the store
// handed back what the registry recorded. Recomputing the tree digest from the
// unpacked files is what closes that, so it has to fail loudly when they differ.
func TestInstallRefusesASubstitutedArchive(t *testing.T) {
	archive := testArchive(t, demoSkill+strings.Repeat("Instructions. ", 40))

	// What it really is.
	dir := t.TempDir()
	pkg, err := verifyInto(archive, "", dir)
	if err != nil {
		t.Fatalf("a valid archive was refused: %v", err)
	}
	real := pkg.Digest
	if real == "" {
		t.Fatal("no digest computed")
	}

	// Claiming to be something else must be refused, and the message must say
	// both digests so the person can tell which end is wrong.
	swapped := t.TempDir()
	_, err = verifyInto(archive, "sha256:"+strings.Repeat("00", 32), swapped)
	if err == nil {
		t.Fatal("an archive that did not match its digest was accepted")
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("unhelpful refusal: %v", err)
	}

	// And the matching case still passes.
	ok := t.TempDir()
	if _, err := verifyInto(archive, real, ok); err != nil {
		t.Errorf("the correct digest was refused: %v", err)
	}
	if _, err := os.Stat(ok + "/SKILL.md"); err != nil {
		t.Errorf("SKILL.md was not unpacked at the root: %v", err)
	}
}

func TestParseRef(t *testing.T) {
	for _, c := range []struct{ in, owner, name, version string }{
		{"xalpha2/flaky-test-hunter", "xalpha2", "flaky-test-hunter", ""},
		{"xalpha2/flaky-test-hunter@1.2.0", "xalpha2", "flaky-test-hunter", "1.2.0"},
		{"flaky-test-hunter", "", "flaky-test-hunter", ""},
		{"flaky-test-hunter@1.0.0", "", "flaky-test-hunter", "1.0.0"},
	} {
		o, n, v := parseRef(c.in)
		if o != c.owner || n != c.name || v != c.version {
			t.Errorf("parseRef(%q) = %q %q %q, want %q %q %q", c.in, o, n, v, c.owner, c.name, c.version)
		}
	}
}

var _ = skill.ShortDigest
