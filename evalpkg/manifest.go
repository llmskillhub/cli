package evalpkg

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	skill "github.com/llmskillhub/cli/skillpkg"
	"gopkg.in/yaml.v3"
)

// Manifest is manifest.yaml.
//
// The shape is the convention's, field for field, so a package written against
// that document validates here without translation -- including the parts this
// catalogue does not itself act on, because dropping them on ingest would mean
// a package came out of the catalogue smaller than it went in.
type Manifest struct {
	Version  string   `yaml:"version"`
	Metadata Metadata `yaml:"metadata"`
	Dataset  Dataset  `yaml:"dataset"`
	Eval     EvalType `yaml:"evaluation_type"`
}

type Metadata struct {
	Name        string   `yaml:"name"`
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	Author      string   `yaml:"author"`
	License     string   `yaml:"license"`
	Tags        []string `yaml:"tags"`
}

type Dataset struct {
	Format   string   `yaml:"format"`
	FilePath string   `yaml:"file_path"`
	Features Features `yaml:"features"`
}

// Features names the columns inside the dataset.
//
// The convention's central idea, and the reason a row here is not assumed to
// look like anyone else's: the manifest says which key carries the prompt and
// which carries the answer, so a medical suite can call them
// patient_symptoms and triage_urgency_level and still be read by a runner that
// has never heard of either.
type Features struct {
	Input          string `yaml:"input"`
	ExpectedOutput string `yaml:"expected_output"`
	Context        string `yaml:"context"`
}

type EvalType struct {
	Method           string   `yaml:"method"`
	TargetJudge      string   `yaml:"target_judge"`
	RubricsDirectory string   `yaml:"rubrics_directory"`
	MetricsDirectory string   `yaml:"metrics_directory"`
	Metrics          []Metric `yaml:"metrics"`
}

// Metric is one bar an eval is graded against. The threshold is what makes a
// result a pass or a failure rather than only a number.
type Metric struct {
	Name      string  `yaml:"name"`
	Threshold float64 `yaml:"threshold"`
}

// Slug is the name this package is published under.
//
// metadata.id first, because the convention treats it as the stable identifier
// and the name as a title. Falling back to the name means a manifest that only
// filled in one of them still publishes.
func (m *Manifest) Slug() string {
	if s := strings.TrimSpace(m.Metadata.ID); s != "" {
		return s
	}
	return slugify(m.Metadata.Name)
}

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case !prevDash && b.Len() > 0:
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// DatasetPath is file_path resolved against the package root.
//
// The convention writes it as "./dataset.jsonl"; a package root is not a
// working directory, so the leading ./ is stripped rather than being treated as
// a directory called ".".
func (m *Manifest) DatasetPath() string {
	p := strings.TrimSpace(m.Dataset.FilePath)
	p = strings.TrimPrefix(p, "./")
	return path.Clean(p)
}

// ParseManifest reads and validates manifest.yaml.
//
// Returns a Manifest even when the Result carries errors, for the same reason
// skillpkg does: the uploader shows as much of the package as parsed, and
// somebody fixing three things wants to see three things.
func ParseManifest(src []byte) (*Manifest, *skill.Result) {
	res := &skill.Result{}
	if !utf8.Valid(src) {
		res.Errorf("manifest_not_utf8", "%s is not valid UTF-8", ManifestFile)
		return &Manifest{}, res
	}
	var m Manifest
	if err := yaml.Unmarshal(src, &m); err != nil {
		res.Add(skill.SeverityError, "manifest_not_yaml",
			fmt.Sprintf("%s is not valid YAML: %v", ManifestFile, err),
			skill.At(ManifestFile, yamlLine(err)))
		return &Manifest{}, res
	}
	validate(&m, res)
	return &m, res
}

func validate(m *Manifest, res *skill.Result) {
	at := skill.At(ManifestFile, 1)

	if strings.TrimSpace(m.Metadata.Name) == "" {
		res.Add(skill.SeverityError, "missing_name", "metadata.name is empty", at)
	}
	if strings.TrimSpace(m.Metadata.Description) == "" {
		res.Add(skill.SeverityError, "missing_description", "metadata.description is empty", at,
			skill.Hint("One sentence saying what this benchmark measures."))
	}
	if m.Slug() == "" {
		res.Add(skill.SeverityError, "missing_id", "metadata.id is empty and the name makes no slug", at,
			skill.Hint("id: medical-qa-alignment"))
	}
	if strings.TrimSpace(m.Version) == "" {
		res.Add(skill.SeverityError, "missing_version", "version is empty", at,
			skill.Hint(`version: "1.0.0"`))
	}

	// The dataset, and the columns inside it.
	switch {
	case m.DatasetPath() == "":
		res.Add(skill.SeverityError, "missing_dataset", "dataset.file_path is empty", at,
			skill.Hint("file_path: ./dataset.jsonl"))
	case !strings.HasSuffix(strings.ToLower(m.DatasetPath()), DatasetExt):
		res.Add(skill.SeverityError, "dataset_not_jsonl",
			fmt.Sprintf("dataset.file_path is %s; only %s is read", m.DatasetPath(), DatasetExt), at)
	}
	if f := strings.ToLower(strings.TrimSpace(m.Dataset.Format)); f != "" && f != "jsonl" {
		res.Add(skill.SeverityError, "unknown_dataset_format",
			fmt.Sprintf("dataset.format is %q; only jsonl is read", m.Dataset.Format), at)
	}
	// Without this the rows cannot be read at all: every consumer learns which
	// key carries the prompt from here and nowhere else.
	if strings.TrimSpace(m.Dataset.Features.Input) == "" {
		res.Add(skill.SeverityError, "missing_input_feature",
			"dataset.features.input does not name a column", at,
			skill.Hint("features:\n    input: \"patient_symptoms\""))
	}
	if strings.TrimSpace(m.Dataset.Features.ExpectedOutput) == "" {
		res.Add(skill.SeverityWarn, "missing_expected_feature",
			"dataset.features.expected_output does not name a column", at,
			skill.Hint("Without it nothing says what a correct answer was supposed to be."))
	}

	// How it is graded.
	if meth := strings.TrimSpace(m.Eval.Method); meth == "" {
		res.Add(skill.SeverityWarn, "missing_method",
			"evaluation_type.method is empty", at,
			skill.Hint("method: "+strings.Join(Methods, " | ")))
	} else if !known(meth, Methods) {
		res.Add(skill.SeverityWarn, "unknown_method",
			fmt.Sprintf("evaluation_type.method is %q, which no runner here recognises", meth), at,
			skill.Hint("Any value is allowed; these are the ones commonly understood: "+
				strings.Join(Methods, ", ")))
	}
	for i, mt := range m.Eval.Metrics {
		if strings.TrimSpace(mt.Name) == "" {
			res.Add(skill.SeverityError, "metric_without_name",
				fmt.Sprintf("evaluation_type.metrics[%d] has no name", i), at)
		}
		// A threshold is what turns a score into a verdict. Outside 0..1 it is
		// either a percentage written as one, or a mistake.
		if mt.Threshold < 0 || mt.Threshold > 1 {
			res.Add(skill.SeverityError, "threshold_out_of_range",
				fmt.Sprintf("metric %q has threshold %g; it is a fraction between 0 and 1",
					mt.Name, mt.Threshold), at,
				skill.Hint("95% is 0.95."))
		}
	}
}

func known(s string, in []string) bool {
	for _, v := range in {
		if v == s {
			return true
		}
	}
	return false
}

func yamlLine(err error) int {
	// "yaml: line 7: ..." is the only position YAML errors carry.
	s := err.Error()
	i := strings.Index(s, "line ")
	if i < 0 {
		return 1
	}
	n := 0
	for _, r := range s[i+5:] {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return 1
	}
	return n
}

// AsSkillManifest projects the fields an eval shares with a skill into the type
// the rest of the pipeline already speaks.
//
// Not a conversion for its own sake: name, description, licence and version
// mean the same thing in both, and they are what the slug, the scanner, the
// version record and the search index are built from. Giving those consumers a
// shape they already handle is what lets an eval travel the storage and review
// path without any of it learning that evals exist.
//
// The eval-specific half -- features, thresholds, the judge -- is deliberately
// not projected. It has no meaning to a skill consumer, and smuggling it
// through this type is how a shared struct becomes a union of two unrelated
// things.
func (m *Manifest) AsSkillManifest() *skill.Manifest {
	if m == nil {
		return nil
	}
	out := &skill.Manifest{
		Name:        m.Slug(),
		Description: m.Metadata.Description,
		License:     m.Metadata.License,
	}
	out.Hub.Version = m.Version
	out.Hub.Keywords = m.Metadata.Tags
	return out
}
