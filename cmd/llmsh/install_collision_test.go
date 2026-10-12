package main

import (
	"strings"
	"testing"

	"github.com/llmskillhub/cli/internal/client"
	skill "github.com/llmskillhub/cli/skillpkg"
)

// Two releases of the same skill, as they would sit on disk after an install.
func twoReleases(t *testing.T) (installed, incoming string, published []client.Version) {
	t.Helper()
	installed, incoming = t.TempDir(), t.TempDir()
	writeFiles(t, installed, map[string]string{"SKILL.md": demoSkill + "First release.\n"})
	writeFiles(t, incoming, map[string]string{"SKILL.md": demoSkill + "Second release.\n"})
	for v, dir := range map[string]string{"0.1.0": installed, "0.1.1": incoming} {
		d, _, err := skill.PackDigest(dir)
		if err != nil {
			t.Fatal(err)
		}
		published = append(published, client.Version{Version: v, Digest: d})
	}
	return installed, incoming, published
}

// TestAnUnmodifiedEarlierReleaseIsUpgradedWithoutForce is the case that made
// this necessary: installing 0.1.1 over an untouched 0.1.0 of the same skill
// asked for --force, as if the directory held a stranger's files.
func TestAnUnmodifiedEarlierReleaseIsUpgradedWithoutForce(t *testing.T) {
	installed, incoming, published := twoReleases(t)
	from, err := collision(installed, "me", "demo", published, incoming)
	if err != nil {
		t.Fatalf("want an upgrade, got %v", err)
	}
	if from != "0.1.0" {
		t.Fatalf("want it to name the release it replaces, got %q", from)
	}
}

// TestALocallyEditedCopyNamesWhatForceWouldLose: edits match no published
// digest, and the refusal lists the files so the person can judge --force.
func TestALocallyEditedCopyNamesWhatForceWouldLose(t *testing.T) {
	installed, incoming, published := twoReleases(t)
	writeFiles(t, installed, map[string]string{
		"SKILL.md":           demoSkill + "First release, with my notes.\n",
		"references/mine.md": "kept locally\n",
	})
	_, err := collision(installed, "me", "demo", published, incoming)
	if err == nil {
		t.Fatal("an edited copy was replaced without --force")
	}
	for _, want := range []string{"modified: SKILL.md", "only here: references/mine.md", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%v", want, err)
		}
	}
}

// TestADifferentSkillInTheDirectoryIsNamed: the same folder name can hold a
// skill that is not this one at all, and saying which is the useful part.
func TestADifferentSkillInTheDirectoryIsNamed(t *testing.T) {
	_, incoming, published := twoReleases(t)
	other := t.TempDir()
	writeFiles(t, other, map[string]string{
		"SKILL.md": strings.Replace(demoSkill, "name: demo", "name: csv-cleaner", 1),
	})
	_, err := collision(other, "me", "demo", published, incoming)
	if err == nil || !strings.Contains(err.Error(), `"csv-cleaner"`) {
		t.Fatalf("want a refusal naming csv-cleaner, got %v", err)
	}
}

// TestWithoutTheVersionListNothingIsReplaced: if the registry can't be asked,
// an unknown directory must still be refused, never assumed to be an upgrade.
func TestWithoutTheVersionListNothingIsReplaced(t *testing.T) {
	installed, incoming, _ := twoReleases(t)
	if _, err := collision(installed, "me", "demo", nil, incoming); err == nil {
		t.Fatal("replaced a directory with nothing to compare it against")
	}
}
