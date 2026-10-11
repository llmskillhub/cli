package skillpkg

import (
	"strings"
	"testing"
)

func parse(t *testing.T, s string) (*Manifest, *Result) {
	t.Helper()
	return ParseManifest([]byte(s))
}

func TestParseValid(t *testing.T) {
	m, res := parse(t, goodSkillMD)
	if !res.OK() {
		t.Fatalf("valid manifest rejected: %s", res)
	}
	if m.Name != "pdf-form-filler" {
		t.Errorf("name = %q", m.Name)
	}
	if m.License != "MIT" {
		t.Errorf("license = %q", m.License)
	}
	if m.Hub.Version != "1.2.0" {
		t.Errorf("hub.version = %q", m.Hub.Version)
	}
	if len(m.Hub.Keywords) != 3 {
		t.Errorf("keywords = %v", m.Hub.Keywords)
	}
	if !strings.Contains(m.Body, "# PDF Form Filler") {
		t.Errorf("body not captured: %q", m.Body)
	}
}

func TestOfficialFieldRules(t *testing.T) {
	cases := []struct {
		name string
		fm   string
		code string
	}{
		{"missing name", "---\ndescription: " + strings.Repeat("x", 50) + "\n---\nbody", "missing_name"},
		{"missing description", "---\nname: ok-name\n---\nbody", "missing_description"},
		{"uppercase name", "---\nname: BadName\ndescription: " + strings.Repeat("x", 50) + "\n---\n", "invalid_name"},
		{"underscore name", "---\nname: bad_name\ndescription: " + strings.Repeat("x", 50) + "\n---\n", "invalid_name"},
		{"leading hyphen", "---\nname: \"-bad\"\ndescription: " + strings.Repeat("x", 50) + "\n---\n", "invalid_name"},
		{"double hyphen", "---\nname: bad--name\ndescription: " + strings.Repeat("x", 50) + "\n---\n", "invalid_name"},
		{"name too long", "---\nname: " + strings.Repeat("a", 65) + "\ndescription: " + strings.Repeat("x", 50) + "\n---\n", "name_too_long"},
		{"angle brackets", "---\nname: ok-name\ndescription: \"see skills/<name>/SKILL.md for the layout and more\"\n---\n", "description_angle_brackets"},
		{"description too long", "---\nname: ok-name\ndescription: " + strings.Repeat("x", 1025) + "\n---\n", "description_too_long"},
		{"compatibility too long", "---\nname: ok-name\ndescription: " + strings.Repeat("x", 50) + "\ncompatibility: " + strings.Repeat("y", 501) + "\n---\n", "compatibility_too_long"},
		{"compatibility as mapping", "---\nname: ok-name\ndescription: " + strings.Repeat("x", 50) + "\ncompatibility:\n  runtimes: [a]\n---\n", "wrong_type"},
		{"unknown key", "---\nname: ok-name\ndescription: " + strings.Repeat("x", 50) + "\nbogus: 1\n---\n", "unknown_frontmatter_key"},
		{"duplicate key", "---\nname: ok-name\nname: other-name\ndescription: " + strings.Repeat("x", 50) + "\n---\n", "duplicate_key"},
		{"no frontmatter", "# just markdown\n", "missing_frontmatter"},
		{"unterminated", "---\nname: ok-name\n", "unterminated_frontmatter"},
		{"not a mapping", "---\n- a\n- b\n---\n", "frontmatter_not_mapping"},
		{"invalid yaml", "---\nname: [unclosed\n---\n", "invalid_yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, res := parse(t, tc.fm)
			found := false
			for _, v := range res.Errors() {
				if v.Code == tc.code {
					found = true
				}
			}
			if !found {
				var got []string
				for _, v := range res.Errors() {
					got = append(got, v.Code)
				}
				t.Fatalf("want %q, got %v", tc.code, got)
			}
		})
	}
}

// TestDriftKeysWarn pins the decision that matters most for adoption: `version`
// appears in 13 of 31 published skills but is not in the official allowlist.
// Erroring on it would reject nearly half the real ecosystem.
func TestDriftKeysWarn(t *testing.T) {
	src := "---\nname: ok-name\ndescription: " + strings.Repeat("x", 50) +
		"\nversion: 0.1.0\nuser-invocable: true\nargument-hint: \"<arg>\"\n---\nbody"
	m, res := parse(t, src)
	if !res.OK() {
		t.Fatalf("drift keys must not block: %s", res)
	}
	if len(res.Warnings()) < 3 {
		t.Errorf("want a warning per drift key, got %d", len(res.Warnings()))
	}
	if m.Hub.Version != "0.1.0" {
		t.Errorf("a top-level version should still be captured, got %q", m.Hub.Version)
	}
	if len(m.DriftKeysFound) != 3 {
		t.Errorf("drift keys = %v", m.DriftKeysFound)
	}
}

// TestDisallowedToolsWarns pins disallowed-tools as a drift key. It arrived in
// the official marketplace (the math-proof plugin's solo and siege skills) next
// to argument-hint and disable-model-invocation, and Claude Code loads those
// skills -- so refusing it would reject a working published package. It is
// held here rather than only in the corpus test, which skips wherever the
// marketplace is not installed, CI included.
func TestDisallowedToolsWarns(t *testing.T) {
	src := "---\nname: solo\ndescription: " + strings.Repeat("x", 50) +
		"\ndisable-model-invocation: true\ndisallowed-tools: WebSearch, WebFetch\n" +
		"allowed-tools: Read, Grep\n---\nbody"
	m, res := parse(t, src)
	if !res.OK() {
		t.Fatalf("disallowed-tools must not block: %s", res)
	}
	found := false
	for _, k := range m.DriftKeysFound {
		if k == "disallowed-tools" {
			found = true
		}
	}
	if !found {
		t.Errorf("disallowed-tools should be reported as a drift key; got %v", m.DriftKeysFound)
	}
	for _, w := range res.Warnings() {
		if w.Code == "nonstandard_frontmatter_key" && strings.Contains(w.Message, "disallowed-tools") && w.Hint == "" {
			t.Error("the warning should say what the field is")
		}
	}
}

func TestLineNumbers(t *testing.T) {
	src := "---\nname: ok-name\ndescription: " + strings.Repeat("x", 50) + "\nbogus: 1\n---\n"
	_, res := parse(t, src)
	for _, v := range res.Errors() {
		if v.Code == "unknown_frontmatter_key" {
			if v.Line != 4 {
				t.Errorf("line = %d, want 4 (the bogus: line)", v.Line)
			}
			if v.Path != SkillFile {
				t.Errorf("path = %q", v.Path)
			}
			return
		}
	}
	t.Fatal("no unknown_frontmatter_key error")
}

func TestCRLFAndBOM(t *testing.T) {
	src := "\xef\xbb\xbf---\r\nname: ok-name\r\ndescription: " + strings.Repeat("x", 50) + "\r\n---\r\nbody\r\n"
	m, res := parse(t, src)
	if !res.OK() {
		t.Fatalf("CRLF + BOM manifest rejected: %s", res)
	}
	if m.Name != "ok-name" {
		t.Errorf("name = %q", m.Name)
	}
}

func TestAllowedToolsForms(t *testing.T) {
	seq := "---\nname: ok-name\ndescription: " + strings.Repeat("x", 50) + "\nallowed-tools:\n  - Read\n  - Bash(git *)\n---\n"
	m, res := parse(t, seq)
	if !res.OK() || len(m.AllowedTools) != 2 {
		t.Fatalf("sequence form: tools=%v res=%s", m.AllowedTools, res)
	}
	// Real skills also use the inline form: allowed-tools: [Read, Glob, Grep]
	inline := "---\nname: ok-name\ndescription: " + strings.Repeat("x", 50) + "\nallowed-tools: [Read, Glob, Grep]\n---\n"
	m2, res2 := parse(t, inline)
	if !res2.OK() || len(m2.AllowedTools) != 3 {
		t.Fatalf("inline form: tools=%v res=%s", m2.AllowedTools, res2)
	}
}

func TestDescriptionScoring(t *testing.T) {
	weak := ScoreDescription("Does PDF stuff.")
	strong := ScoreDescription(goodDescription)
	if strong.Total <= weak.Total {
		t.Errorf("strong=%d weak=%d", strong.Total, weak.Total)
	}
	if !strong.HasWhen {
		t.Error("strong description should have a when-clause")
	}
	if !strong.Pushy {
		t.Error("strong description should cover the non-obvious trigger")
	}
	if second := ScoreDescription("Use this when you want to fill your PDF forms quickly today"); second.ThirdPerson {
		t.Error("second person should be detected")
	}
}

const goodDescription = `Fills, flattens and validates AcroForm and XFA PDF forms. Use this skill whenever the user mentions "fill a PDF form" or "flatten a PDF", even if they do not say the word form.`

// The namespace was renamed from skillhub to llmskillhub. Nothing published
// used the old key at the time, but the guide had shown it, and a package
// someone wrote against that page should not become invalid because we
// changed our mind about a name.
func TestHubNamespaceAcceptsBothKeys(t *testing.T) {
	body := "\n\n# Title\n\n" + strings.Repeat("Body text that is long enough to be a real skill. ", 12)

	for _, key := range []string{HubKey, HubKeyLegacy} {
		t.Run(key, func(t *testing.T) {
			src := []byte("---\nname: pdf-tools\n" +
				"description: Fills PDF forms from structured data and extracts values back out. " +
				"Use when the user mentions PDF forms or filling a PDF.\n" +
				"metadata:\n  " + key + ":\n    version: 1.2.0\n" +
				"    categories: [documents]\n    capabilities:\n      network: true\n" +
				"      shell: false\n      filesystem: read\n---" + body)

			m, res := ParseManifest(src)
			for _, v := range res.Violations {
				if v.Severity == SeverityError {
					t.Fatalf("%s: %s — %s", key, v.Code, v.Message)
				}
			}
			if m.Hub.Version != "1.2.0" {
				t.Errorf("version = %q", m.Hub.Version)
			}
			if len(m.Hub.Categories) != 1 || m.Hub.Categories[0] != "documents" {
				t.Errorf("categories = %v", m.Hub.Categories)
			}
			if !m.Hub.Capabilities.Network || m.Hub.Capabilities.Filesystem != "read" {
				t.Errorf("capabilities = %+v", m.Hub.Capabilities)
			}
		})
	}
}

// Both at once is not a shape to encourage, but it has one obvious right
// answer: the key we ask for now.
func TestTheCurrentHubKeyWins(t *testing.T) {
	body := "\n\n# Title\n\n" + strings.Repeat("Body text that is long enough to be a real skill. ", 12)
	src := []byte("---\nname: pdf-tools\n" +
		"description: Fills PDF forms from structured data and extracts values back out. " +
		"Use when the user mentions PDF forms or filling a PDF.\n" +
		"metadata:\n  skillhub:\n    version: 0.0.1\n  llmskillhub:\n    version: 2.0.0\n---" + body)

	m, res := ParseManifest(src)
	for _, v := range res.Violations {
		if v.Severity == SeverityError {
			t.Fatalf("%s — %s", v.Code, v.Message)
		}
	}
	if m.Hub.Version != "2.0.0" {
		t.Errorf("version = %q, want the llmskillhub one", m.Hub.Version)
	}
}

// Whether a manifest said anything about capabilities is a different answer
// from what it said, and the scanner needs both. It has to read the same
// namespace the typed fields came from -- the current key, the old one, and the
// current one when a manifest somehow carries both.
func TestCapabilitiesStatedReadsTheSameKeyAsTheHubFields(t *testing.T) {
	body := "\n\n# Title\n\n" + strings.Repeat("Body text that is long enough to be a real skill. ", 12)
	head := "---\nname: pdf-tools\n" +
		"description: Fills PDF forms from structured data and extracts values back out. " +
		"Use when the user mentions PDF forms or filling a PDF.\n"
	cases := map[string]struct {
		meta string
		want bool
	}{
		"current key with capabilities": {"metadata:\n  llmskillhub:\n    capabilities:\n      network: false\n", true},
		"old key with capabilities":     {"metadata:\n  skillhub:\n    capabilities:\n      network: false\n", true},
		"current key, no capabilities":  {"metadata:\n  llmskillhub:\n    version: 1.0.0\n", false},
		"no hub block at all":           {"", false},
		// The current key wins for the typed fields, so it wins here too: a
		// stale old block cannot make a silent manifest look like a claim.
		"both, only the old one states": {"metadata:\n  skillhub:\n    capabilities:\n      network: false\n  llmskillhub:\n    version: 1.0.0\n", false},
		"both, only the new one states": {"metadata:\n  skillhub:\n    version: 1.0.0\n  llmskillhub:\n    capabilities:\n      shell: false\n", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m, res := ParseManifest([]byte(head + c.meta + "---" + body))
			for _, v := range res.Violations {
				if v.Severity == SeverityError {
					t.Fatalf("%s — %s", v.Code, v.Message)
				}
			}
			if got := m.CapabilitiesStated(); got != c.want {
				t.Errorf("CapabilitiesStated() = %v, want %v", got, c.want)
			}
		})
	}
	var nilManifest *Manifest
	if nilManifest.CapabilitiesStated() {
		t.Error("a nil manifest states nothing")
	}
}
