package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skill "github.com/llmskillhub/cli/skillpkg"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func anEval(t *testing.T) (archive []byte, digest string) {
	t.Helper()
	d := t.TempDir()
	writeFiles(t, d, map[string]string{
		"manifest.yaml": `version: "1.0.0"
metadata:
  name: "Kind check"
  id: "kind-check"
  description: "An eval used to check that install routes on the declared kind."
  license: "MIT"
dataset:
  format: "jsonl"
  file_path: "./dataset.jsonl"
  features:
    input: "q"
    expected_output: "a"
evaluation_type:
  method: "llm_as_a_judge"
  target_judge: "gpt-4o"
  rubrics_directory: "./rubrics"
  metrics:
    - name: "Correct"
      threshold: 0.9
`,
		"dataset.jsonl":   `{"id":"1","q":"two plus two","a":"four"}` + "\n",
		"rubrics/r.md":    "Judge whether the answer is right.\n",
	})
	pkg, _, archive, err := packEval(d)
	if err != nil {
		t.Fatalf("pack eval: %v", err)
	}
	return archive, pkg.Digest
}

func aSkill(t *testing.T) []byte {
	t.Helper()
	d := t.TempDir()
	writeFiles(t, d, map[string]string{"SKILL.md": `---
name: not-an-eval
description: A skill, used to check that a package claiming to be an eval is refused when it is not one. Use when testing install.
license: MIT
metadata:
  llmskillhub:
    version: 1.0.0
---

# not an eval

A skill wearing the wrong label.

## When to reach for this

When a test needs a skill to be mistaken for an eval.
`})
	var buf bytes.Buffer
	if _, _, err := skill.Pack(d, &buf, skill.PackOptions{RootName: "not-an-eval"}); err != nil {
		t.Fatalf("pack skill: %v", err)
	}
	return buf.Bytes()
}

func nothingWritten(t *testing.T, dest string) {
	t.Helper()
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Errorf("%d thing(s) written to %s; a refused package must not reach the disk", len(entries), dest)
	}
}

// The header says what a package is; the validator for that kind has the last
// word. A skill delivered as an eval is refused, and nothing lands in ./evals.
func TestAPackageThatIsNotWhatTheServerSaidIsRefused(t *testing.T) {
	dest := t.TempDir()
	err := installEval("alice", "not-an-eval", aSkill(t), "", dest, false)
	if err == nil {
		t.Fatal("a skill was accepted as an eval")
	}
	nothingWritten(t, dest)
}

// eval get printed the expected digest and unpacked whatever arrived. Now a
// mismatch is refused before anything is written, the way install refuses one.
func TestAnEvalWhoseDigestDoesNotMatchIsRefused(t *testing.T) {
	archive, _ := anEval(t)
	dest := t.TempDir()
	err := installEval("alice", "kind-check", archive, "sha256:"+strings.Repeat("0", 64), dest, false)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("a wrong digest was accepted: %v", err)
	}
	nothingWritten(t, dest)
}

// And a genuine eval with the right digest goes where evals go.
func TestAGenuineEvalIsUnpackedUnderEvals(t *testing.T) {
	archive, digest := anEval(t)
	dest := t.TempDir()
	if err := installEval("alice", "kind-check", archive, digest, dest, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "kind-check", "manifest.yaml")); err != nil {
		t.Errorf("the eval was not unpacked under %s/kind-check: %v", dest, err)
	}
}
