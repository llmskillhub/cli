package skillpkg

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// OfficialKeys is the closed allowlist enforced by the official skill validator.
// Any key outside it fails validation there, which is why marketplace metadata
// lives under `metadata:` rather than as new top-level keys.
var OfficialKeys = map[string]bool{
	"name": true, "description": true, "license": true,
	"allowed-tools": true, "metadata": true, "compatibility": true,
}

// DriftKeys are keys that appear in real published skills but are NOT in the
// official allowlist — the Claude Code plugin loader tolerates them, the
// standalone validator does not. `version` alone appears in 13 of 31 published
// skills, so treating these as errors would reject a large slice of the real
// ecosystem. They warn instead.
//
// disallowed-tools joined when the official marketplace shipped it (the
// math-proof plugin) beside argument-hint and disable-model-invocation: a
// Claude Code plugin field like them, on skills Claude Code loads.
var DriftKeys = map[string]bool{
	"version": true, "user-invocable": true, "tools": true,
	"argument-hint": true, "disable-model-invocation": true,
	"disallowed-tools": true,
}

var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// OfficialRuleCodes are the violations that mirror quick_validate.py exactly.
// The distinction matters: a package rejected on one of these would also be
// rejected by the official tooling, so refusing it is correct. A package rejected
// on any OTHER error code is us being stricter than the runtime, which is the one
// thing the validator must never do.
var OfficialRuleCodes = map[string]bool{
	"missing_frontmatter": true, "unterminated_frontmatter": true, "invalid_yaml": true,
	"empty_frontmatter": true, "frontmatter_not_mapping": true, "unknown_frontmatter_key": true,
	"missing_name": true, "empty_name": true, "invalid_name": true, "name_too_long": true,
	"missing_description": true, "empty_description": true,
	"description_angle_brackets": true, "description_too_long": true,
	"compatibility_too_long": true, "wrong_type": true,
}

type Author struct {
	Name   string `yaml:"name" json:"name"`
	Handle string `yaml:"handle" json:"handle,omitempty"`
	Email  string `yaml:"email" json:"email,omitempty"`
}

type Capabilities struct {
	Network    bool     `yaml:"network" json:"network"`
	Filesystem string   `yaml:"filesystem" json:"filesystem,omitempty"` // none|read|read-write
	Shell      bool     `yaml:"shell" json:"shell"`
	Secrets    []string `yaml:"secrets" json:"secrets,omitempty"`
}

// HubMetadata is our namespace inside the manifest's `metadata` map. Namespacing
// under HubKey rather than at metadata's root keeps us from colliding with
// anyone else who uses that field.
type HubMetadata struct {
	Version      string       `yaml:"version" json:"version,omitempty"`
	Categories   []string     `yaml:"categories" json:"categories,omitempty"`
	Keywords     []string     `yaml:"keywords" json:"keywords,omitempty"`
	Homepage     string       `yaml:"homepage" json:"homepage,omitempty"`
	Repository   string       `yaml:"repository" json:"repository,omitempty"`
	Authors      []Author     `yaml:"authors" json:"authors,omitempty"`
	Capabilities Capabilities `yaml:"capabilities" json:"capabilities"`
}

type Manifest struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	License       string         `json:"license,omitempty"`
	AllowedTools  []string       `json:"allowed_tools,omitempty"`
	Compatibility string         `json:"compatibility,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`

	Hub HubMetadata `json:"hub"`

	// Body is everything after the frontmatter — the instructions themselves.
	Body string `json:"-"`
	// BodyWords drives the progressive-disclosure checks.
	BodyWords int `json:"body_words"`
	// DriftKeysFound records non-standard keys, for a warning and for the UI.
	DriftKeysFound []string `json:"drift_keys,omitempty"`
}

var frontmatterRe = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?`)

// ParseManifest splits SKILL.md into frontmatter and body and validates the
// frontmatter against the official rules. It returns a Manifest even when the
// Result carries errors, so callers can show as much of the package as parsed.
func ParseManifest(src []byte) (*Manifest, *Result) {
	res := &Result{}
	m := &Manifest{}

	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf")) // strip UTF-8 BOM

	if !utf8.Valid(src) {
		res.Errorf("skill_md_not_utf8", "SKILL.md is not valid UTF-8")
		return m, res
	}
	if !bytes.HasPrefix(src, []byte("---")) {
		res.Add(SeverityError, "missing_frontmatter",
			"SKILL.md must begin with a YAML frontmatter block delimited by ---",
			At(SkillFile, 1),
			Hint("Start the file with:\n---\nname: my-skill\ndescription: ...\n---"))
		return m, res
	}
	loc := frontmatterRe.FindSubmatchIndex(src)
	if loc == nil {
		res.Add(SeverityError, "unterminated_frontmatter",
			"the frontmatter block is opened but never closed with a --- line",
			At(SkillFile, 1))
		return m, res
	}
	fm := src[loc[2]:loc[3]]
	m.Body = strings.TrimLeft(string(src[loc[1]:]), "\r\n")
	m.BodyWords = len(strings.Fields(m.Body))

	var root yaml.Node
	if err := yaml.Unmarshal(fm, &root); err != nil {
		res.Add(SeverityError, "invalid_yaml",
			fmt.Sprintf("frontmatter is not valid YAML: %v", err),
			At(SkillFile, yamlErrLine(err)))
		return m, res
	}
	if len(root.Content) == 0 {
		res.Add(SeverityError, "empty_frontmatter", "frontmatter is empty", At(SkillFile, 1))
		return m, res
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		res.Add(SeverityError, "frontmatter_not_mapping",
			"frontmatter must be a YAML mapping of key: value pairs",
			At(SkillFile, doc.Line+1))
		return m, res
	}

	// Frontmatter line numbers are offset by the opening --- line.
	const fmOffset = 1
	seen := map[string]int{}

	for i := 0; i+1 < len(doc.Content); i += 2 {
		k, v := doc.Content[i], doc.Content[i+1]
		key := k.Value
		line := k.Line + fmOffset

		if prev, dup := seen[key]; dup {
			res.Add(SeverityError, "duplicate_key",
				fmt.Sprintf("duplicate frontmatter key %q (first seen on line %d)", key, prev),
				At(SkillFile, line))
			continue
		}
		seen[key] = line

		switch {
		case OfficialKeys[key]:
			decodeOfficial(m, key, v, line, res)
		case DriftKeys[key]:
			m.DriftKeysFound = append(m.DriftKeysFound, key)
			res.Add(SeverityWarn, "nonstandard_frontmatter_key",
				fmt.Sprintf("%q is not part of the skill standard; it works in Claude Code plugins but other loaders may reject the package", key),
				At(SkillFile, line),
				Hint(driftHint(key)))
			if key == "version" {
				// Capture it anyway — it is the creator's stated intent, and the
				// publish flow reconciles it against the requested version.
				m.Hub.Version = strings.TrimSpace(v.Value)
			}
		default:
			res.Add(SeverityError, "unknown_frontmatter_key",
				fmt.Sprintf("unknown frontmatter key %q", key),
				At(SkillFile, line),
				Hint("Allowed keys: name, description, license, allowed-tools, compatibility, metadata. "+
					"Marketplace fields belong under metadata."+HubKey+"."))
		}
	}

	validateManifest(m, seen, res)
	return m, res
}

func decodeOfficial(m *Manifest, key string, v *yaml.Node, line int, res *Result) {
	fail := func(want string) {
		res.Add(SeverityError, "wrong_type",
			fmt.Sprintf("%q must be %s", key, want), At(SkillFile, line))
	}
	switch key {
	case "name":
		if v.Kind != yaml.ScalarNode {
			fail("a string")
			return
		}
		m.Name = strings.TrimSpace(v.Value)
	case "description":
		if v.Kind != yaml.ScalarNode {
			fail("a string")
			return
		}
		m.Description = strings.TrimSpace(v.Value)
	case "license":
		if v.Kind != yaml.ScalarNode {
			fail("a string")
			return
		}
		m.License = strings.TrimSpace(v.Value)
	case "compatibility":
		if v.Kind != yaml.ScalarNode {
			fail("a string (the official validator rejects a mapping here)")
			return
		}
		m.Compatibility = strings.TrimSpace(v.Value)
	case "allowed-tools":
		switch v.Kind {
		case yaml.SequenceNode:
			for _, e := range v.Content {
				m.AllowedTools = append(m.AllowedTools, strings.TrimSpace(e.Value))
			}
		case yaml.ScalarNode:
			for _, s := range strings.Split(v.Value, ",") {
				if s = strings.TrimSpace(s); s != "" {
					m.AllowedTools = append(m.AllowedTools, s)
				}
			}
		default:
			fail("a list of tool patterns")
		}
	case "metadata":
		if v.Kind != yaml.MappingNode {
			fail("a mapping")
			return
		}
		if err := v.Decode(&m.Metadata); err != nil {
			res.Add(SeverityError, "invalid_metadata",
				fmt.Sprintf("metadata could not be decoded: %v", err), At(SkillFile, line))
			return
		}
		decodeHub(m, v, line, res)
	}
}

// HubKey is the namespace this marketplace's fields live under inside the
// spec's metadata map, and HubKeyLegacy is what it used to be.
//
// The spec asks for a key "reasonably unique to avoid accidental conflicts",
// and skillhub is not: it is a common enough word that another tool in this
// same space already publishes under it. llmskillhub is the domain, so it is
// ours in a way the short form never was.
const (
	HubKey       = "llmskillhub"
	HubKeyLegacy = "skillhub"
)

// CapabilitiesStated reports whether the manifest carried a capabilities block
// at all.
//
// The typed struct cannot answer this: "network: false" and a manifest that
// never mentioned the network are the same zero value, and the scanner grades
// those differently -- accusing someone of contradicting a claim they never
// made is how a scanner loses its audience. The raw metadata map still has the
// shape of what was written, so the question is answerable there.
//
// It reads the namespace decodeHub reads, by the same rule: the current key,
// else the old one. Asking only the old key -- which is what the scanners did
// -- treated every manifest written against the current guide as silent.
func (m *Manifest) CapabilitiesStated() bool {
	if m == nil {
		return false
	}
	hub, ok := m.Metadata[HubKey].(map[string]any)
	if !ok {
		hub, ok = m.Metadata[HubKeyLegacy].(map[string]any)
	}
	if !ok {
		return false
	}
	_, ok = hub["capabilities"]
	return ok
}

// decodeHub pulls metadata.llmskillhub into typed form. Unknown keys under our
// own namespace are tolerated — this is our field and we may add to it.
//
// The old key still works. Nothing published used it when the name changed,
// but the guide had shown it and a package written against that page should
// not become invalid because we renamed something. The new key wins if a
// manifest somehow carries both.
func decodeHub(m *Manifest, metaNode *yaml.Node, line int, res *Result) {
	var node *yaml.Node
	for i := 0; i+1 < len(metaNode.Content); i += 2 {
		switch metaNode.Content[i].Value {
		case HubKey:
			node = metaNode.Content[i+1]
		case HubKeyLegacy:
			if node == nil {
				node = metaNode.Content[i+1]
			}
		}
	}
	if node == nil {
		return
	}
	if node.Kind != yaml.MappingNode {
		res.Add(SeverityError, "invalid_hub_metadata",
			"metadata."+HubKey+" must be a mapping", At(SkillFile, node.Line+1))
		return
	}
	if err := node.Decode(&m.Hub); err != nil {
		res.Add(SeverityError, "invalid_hub_metadata",
			fmt.Sprintf("metadata.%s could not be decoded: %v", HubKey, err),
			At(SkillFile, node.Line+1))
	}
}

// validateManifest applies the official field rules verbatim. Every check here
// mirrors quick_validate.py; the limits are its limits, not ours.
func validateManifest(m *Manifest, seen map[string]int, res *Result) {
	nameLine, descLine := seen["name"], seen["description"]

	if _, ok := seen["name"]; !ok {
		res.Add(SeverityError, "missing_name", "frontmatter is missing required key 'name'", At(SkillFile, 1))
	} else {
		switch {
		case m.Name == "":
			res.Add(SeverityError, "empty_name", "'name' must not be empty", At(SkillFile, nameLine))
		case !nameRe.MatchString(m.Name):
			res.Add(SeverityError, "invalid_name",
				fmt.Sprintf("name %q must be kebab-case: lowercase letters, digits and hyphens only", m.Name),
				At(SkillFile, nameLine), Hint("e.g. pdf-form-filler"))
		case strings.HasPrefix(m.Name, "-"), strings.HasSuffix(m.Name, "-"), strings.Contains(m.Name, "--"):
			res.Add(SeverityError, "invalid_name",
				fmt.Sprintf("name %q cannot start or end with a hyphen, or contain consecutive hyphens", m.Name),
				At(SkillFile, nameLine))
		case len(m.Name) > MaxNameLen:
			res.Add(SeverityError, "name_too_long",
				fmt.Sprintf("name is %d characters; the maximum is %d", len(m.Name), MaxNameLen),
				At(SkillFile, nameLine))
		}
	}

	if _, ok := seen["description"]; !ok {
		res.Add(SeverityError, "missing_description",
			"frontmatter is missing required key 'description'", At(SkillFile, 1))
		return
	}
	d := m.Description
	switch {
	case d == "":
		res.Add(SeverityError, "empty_description", "'description' must not be empty", At(SkillFile, descLine))
	case strings.ContainsAny(d, "<>"):
		res.Add(SeverityError, "description_angle_brackets",
			"description must not contain angle brackets (< or >)",
			At(SkillFile, descLine),
			Hint("Descriptions are embedded in prompt scaffolding, where angle brackets are "+
				"reserved. Placeholders like skills/<name>/SKILL.md are the usual cause — "+
				"write skills/{name}/SKILL.md or use backticks instead."))
	case len(d) > MaxDescriptionLen:
		res.Add(SeverityError, "description_too_long",
			fmt.Sprintf("description is %d characters; the maximum is %d", len(d), MaxDescriptionLen),
			At(SkillFile, descLine))
	}
	if m.Compatibility != "" && len(m.Compatibility) > MaxCompatLen {
		res.Add(SeverityError, "compatibility_too_long",
			fmt.Sprintf("compatibility is %d characters; the maximum is %d", len(m.Compatibility), MaxCompatLen),
			At(SkillFile, seen["compatibility"]))
	}
}

func driftHint(key string) string {
	switch key {
	case "version":
		return "Move it to metadata.llmskillhub.version, or pass --version at publish time."
	case "user-invocable", "argument-hint", "disable-model-invocation", "tools", "disallowed-tools":
		return "This is a Claude Code plugin field. Keep it if you ship as a plugin; it is ignored elsewhere."
	}
	return ""
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

func yamlErrLine(err error) int {
	if mm := yamlLineRe.FindStringSubmatch(err.Error()); mm != nil {
		var n int
		fmt.Sscanf(mm[1], "%d", &n)
		return n + 1 // account for the opening --- line
	}
	return 0
}
