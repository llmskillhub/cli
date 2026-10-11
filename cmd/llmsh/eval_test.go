package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheSwitchDefaultsOn pins the default now that evals have launched:
// unset is on, and only an explicit "no" turns them off, for a self-hosted
// catalogue without evals.
func TestTheSwitchDefaultsOn(t *testing.T) {
	cases := map[string]bool{
		"1": true, "true": true, "yes": true, "on": true, "": true, "anything": true,
		"0": false, "false": false, "FALSE": false, "no": false, "off": false, " off ": false,
	}
	for in, want := range cases {
		t.Setenv("LLMSH_EVALS", in)
		if got := evalsEnabled(); got != want {
			t.Errorf("LLMSH_EVALS=%q -> %v, want %v", in, got, want)
		}
	}
	os.Unsetenv("LLMSH_EVALS")
	if !evalsEnabled() {
		t.Error("unset must be on")
	}
}

// TestTheDirectoryDecidesTheKind is the rule publish branches on.
//
// A manifest.yaml makes it an eval; anything else is a skill, which is what
// every directory was before there was a second kind. Checked against real
// files rather than a string, because the failure worth catching is a
// directory NAMED manifest.yaml, which is not a manifest.
func TestTheDirectoryDecidesTheKind(t *testing.T) {
	t.Run("a manifest makes it an eval", func(t *testing.T) {
		d := t.TempDir()
		writeEvalFile(t, filepath.Join(d, "manifest.yaml"), "version: \"1.0.0\"\n")
		if !isEvalDir(d) {
			t.Error("manifest.yaml should mean eval")
		}
	})
	t.Run("the .yml spelling counts too", func(t *testing.T) {
		d := t.TempDir()
		writeEvalFile(t, filepath.Join(d, "manifest.yml"), "version: \"1.0.0\"\n")
		if !isEvalDir(d) {
			t.Error("manifest.yml should mean eval")
		}
	})
	t.Run("a skill is not an eval", func(t *testing.T) {
		d := t.TempDir()
		writeEvalFile(t, filepath.Join(d, "SKILL.md"), "---\nname: x\n---\n")
		if isEvalDir(d) {
			t.Error("a SKILL.md directory must stay a skill")
		}
	})
	t.Run("a DIRECTORY called manifest.yaml is not a manifest", func(t *testing.T) {
		d := t.TempDir()
		if err := os.Mkdir(filepath.Join(d, "manifest.yaml"), 0o755); err != nil {
			t.Fatal(err)
		}
		if isEvalDir(d) {
			t.Error("a directory named manifest.yaml must not make this an eval")
		}
	})
}

// TestTheRefusalNamesBothSwitches. Setting only the CLI's half produces a
// publish the server refuses, so the message has to mention the other one --
// otherwise the next thing that happens is a confusing 404.
func TestTheRefusalNamesBothSwitches(t *testing.T) {
	msg := evalRefusal("./some-eval").Error()
	for _, want := range []string{"LLMSH_EVALS", "EVALS_ENABLED", "manifest.yaml"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %s:\n%s", want, msg)
		}
	}
}

func writeEvalFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
