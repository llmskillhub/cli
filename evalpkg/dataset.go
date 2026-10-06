package evalpkg

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	skill "github.com/llmskillhub/cli/skillpkg"
)

// DatasetReport is what one dataset amounts to, and what the review screen
// renders.
//
// Counts rather than flags. A skill's capabilities describe one document a
// reviewer is already reading; an eval's describe a file too long to read by
// eye, so the number has to be computed and put in front of them. "12 of 240
// samples run a setup script" is actionable in a way that "contains scripts" is
// not.
type DatasetReport struct {
	Path        string   `json:"path"`
	Samples     int      `json:"samples"`
	WithTarget  int      `json:"with_target"`
	WithContext int      `json:"with_context"`
	WithSetup   int      `json:"with_setup"`
	WithSandbox int      `json:"with_sandbox"`
	WithFiles   int      `json:"with_files"`
	RemoteFiles []string `json:"remote_files,omitempty"`
	HiddenRunes int      `json:"hidden_runes"`
	// ExtraKeys are columns present in the rows that the manifest never named.
	// Not an error -- a dataset may carry provenance nobody grades on -- but a
	// reviewer should see them, because an undeclared column is also how a
	// field gets read by something that was not supposed to read it.
	ExtraKeys []string `json:"extra_keys,omitempty"`
}

func (d DatasetReport) Executes() bool {
	return d.WithSetup > 0 || d.WithSandbox > 0 || d.WithFiles > 0
}

// ReadDataset validates one dataset against the columns its manifest declared.
//
// Streamed a line at a time rather than read whole: the cap allows 32 MB, and
// holding that in memory to count rows would be a choice we would regret on the
// first large benchmark.
func ReadDataset(r io.Reader, path string, f Features, inPackage map[string]bool,
	res *skill.Result) DatasetReport {

	rep := DatasetReport{Path: path}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxSampleBytes)

	declared := map[string]bool{}
	for _, k := range []string{f.Input, f.ExpectedOutput, f.Context} {
		if k = strings.TrimSpace(k); k != "" {
			declared[k] = true
		}
	}
	// The three the convention borrows from Inspect for sandboxed runs. Known
	// rather than declared, because they say how a sample runs rather than what
	// it contains.
	for _, k := range []string{"id", "files", "setup", "sandbox"} {
		declared[k] = true
	}

	extra := map[string]bool{}
	seenID := map[string]int{}
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue // a trailing newline is not a sample
		}
		if rep.Samples >= MaxSamples {
			res.Add(skill.SeverityError, "too_many_samples",
				fmt.Sprintf("%s has more than %d samples", path, MaxSamples), skill.At(path, line))
			return rep
		}

		var row map[string]json.RawMessage
		if err := json.Unmarshal(raw, &row); err != nil {
			res.Add(skill.SeverityError, "sample_not_json",
				fmt.Sprintf("line %d is not a JSON object: %v", line, err), skill.At(path, line),
				skill.Hint("One JSON object per line. A whole array on one line is JSON, not JSONL."))
			return rep
		}
		rep.Samples++
		checkRow(row, f, path, line, inPackage, &rep, res)

		for k := range row {
			if !declared[k] {
				extra[k] = true
			}
		}
		if idRaw, ok := row["id"]; ok {
			var id string
			if json.Unmarshal(idRaw, &id) == nil && id != "" {
				if first, dup := seenID[id]; dup {
					res.Add(skill.SeverityError, "duplicate_sample_id",
						fmt.Sprintf("id %q is already used on line %d", id, first),
						skill.At(path, line))
				} else {
					seenID[id] = line
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			res.Add(skill.SeverityError, "sample_too_large",
				fmt.Sprintf("a line in %s is longer than %s", path, humanBytes(MaxSampleBytes)),
				skill.At(path, line+1),
				skill.Hint("A sample that large is usually an attachment, or a JSON array written on one line."))
		} else {
			res.Add(skill.SeverityError, "dataset_unreadable",
				fmt.Sprintf("%s could not be read: %v", path, err), skill.At(path, 0))
		}
		return rep
	}
	if rep.Samples == 0 {
		res.Add(skill.SeverityError, "empty_dataset",
			fmt.Sprintf("%s contains no samples", path), skill.At(path, 0))
	}
	for k := range extra {
		rep.ExtraKeys = append(rep.ExtraKeys, k)
	}
	sort.Strings(rep.ExtraKeys)
	sort.Strings(rep.RemoteFiles)
	return rep
}

func checkRow(row map[string]json.RawMessage, f Features, path string, line int,
	inPackage map[string]bool, rep *DatasetReport, res *skill.Result) {

	// The declared input column is the only one a row cannot do without: it is
	// what gets submitted to the model, and a row without it is not a test of
	// anything.
	in, ok := row[f.Input]
	switch {
	case !ok || len(in) == 0:
		res.Add(skill.SeverityError, "sample_no_input",
			fmt.Sprintf("line %d has no %q, the column the manifest names as the input",
				line, f.Input), skill.At(path, line))
	case !isStringOrMessages(in):
		res.Add(skill.SeverityError, "sample_bad_input",
			fmt.Sprintf("line %d: %q must be a string or a list of {role, content}", line, f.Input),
			skill.At(path, line))
	}

	if f.ExpectedOutput != "" {
		if v, ok := row[f.ExpectedOutput]; ok && len(v) > 0 {
			rep.WithTarget++
		}
	}
	if f.Context != "" {
		if v, ok := row[f.Context]; ok && len(v) > 0 {
			rep.WithContext++
		}
	}

	// Hidden and bidirectional characters, in the text that reaches a model.
	// The same rule a skill's prose passes through, and it matters more here:
	// nobody reads row 147 by eye, so an instruction hidden in one would be
	// carried past a reviewer who was reading the manifest.
	n := len(skill.HiddenRunes(textOf(row[f.Input])))
	if f.ExpectedOutput != "" {
		n += len(skill.HiddenRunes(textOf(row[f.ExpectedOutput])))
	}
	if n > 0 {
		rep.HiddenRunes += n
		res.Add(skill.SeverityWarn, "sample_hidden_characters",
			fmt.Sprintf("line %d contains %d invisible or direction-changing characters", line, n),
			skill.At(path, line),
			skill.Hint("These render as nothing and can carry instructions a reader will not see."))
	}

	if v, ok := row["setup"]; ok && len(v) > 0 && string(v) != `""` {
		rep.WithSetup++
	}
	if v, ok := row["sandbox"]; ok && len(v) > 0 {
		rep.WithSandbox++
	}
	if v, ok := row["files"]; ok && len(v) > 0 {
		var files map[string]string
		if json.Unmarshal(v, &files) == nil && len(files) > 0 {
			rep.WithFiles++
			for dest, src := range files {
				switch {
				case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "https://"):
					// Content the package does not contain, fetched later, that
					// no reviewer saw. Allowed when declared; never quiet.
					rep.RemoteFiles = append(rep.RemoteFiles, src)
					continue
				case inPackage == nil:
				case !inPackage[src]:
					res.Add(skill.SeverityError, "sample_file_missing",
						fmt.Sprintf("line %d maps %s from %q, which is not in the package",
							line, dest, src), skill.At(path, line))
				}
				if err := skill.ValidateEntryPath(src); err != nil {
					res.Add(skill.SeverityError, "sample_file_path",
						fmt.Sprintf("line %d: %q is not a usable path: %v", line, src, err),
						skill.At(path, line))
				}
			}
		}
	}
}

// textOf decodes a field to the text a model would actually see.
//
// Checking the raw JSON instead would find nothing: a zero-width space arrives
// as the six ASCII characters ​, which are not hidden and not a problem.
// The character only exists after decoding, which is also the only form the
// model is ever shown.
func textOf(b json.RawMessage) string {
	if len(b) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(b, &str) == nil {
		return str
	}
	var msgs []struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(b, &msgs) == nil {
		var sb strings.Builder
		for _, m := range msgs {
			sb.WriteString(textOf(m.Content))
			sb.WriteByte('\n')
		}
		return sb.String()
	}
	var list []string
	if json.Unmarshal(b, &list) == nil {
		return strings.Join(list, "\n")
	}
	return ""
}

func isStringOrMessages(b json.RawMessage) bool {
	var s string
	if json.Unmarshal(b, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(b, &msgs) != nil || len(msgs) == 0 {
		return false
	}
	for _, m := range msgs {
		if m.Role == "" || len(m.Content) == 0 {
			return false
		}
	}
	return true
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
