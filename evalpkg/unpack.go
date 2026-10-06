package evalpkg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	skill "github.com/llmskillhub/cli/skillpkg"
)

// EvalShape tells skillpkg what an eval package looks like.
//
// No KeepRoots: this convention puts the dataset at the package root and its
// rubrics beside it, so nothing the packer strips is load-bearing here.
var EvalShape = skill.Shape{
	Required:    ManifestFile,
	AlsoAccept:  []string{ManifestFileAlt},
	MissingCode: "missing_manifest",
}

// Package is an unpacked eval: the files, as skillpkg extracted them, plus what
// reading the manifest and the dataset found.
type Package struct {
	*skill.Package
	Eval    *Manifest      `json:"eval"`
	Dataset *DatasetReport `json:"dataset,omitempty"`
	// Rubrics and Metrics are the files the manifest points at, listed rather
	// than read: a reviewer opens them, and this only has to say they exist.
	Rubrics []string `json:"rubrics,omitempty"`
	Metrics []string `json:"metrics,omitempty"`
}

func (p *Package) Samples() int {
	if p.Dataset == nil {
		return 0
	}
	return p.Dataset.Samples
}

func (p *Package) Executes() bool {
	return (p.Dataset != nil && p.Dataset.Executes()) || len(p.Metrics) > 0
}

// Unpack inflates an archive and validates it as an eval.
//
// The extraction is skillpkg.UnpackFiles -- the same ratio guard, the same path
// safety, the same refusal of an archive two readers disagree about. What
// happens after it is entirely different from a skill's, which is the reason
// this is a separate entry point rather than a flag on that one.
func Unpack(ctx context.Context, ra io.ReaderAt, size int64, dir string) (*Package, *skill.Result, error) {
	base, res, err := skill.UnpackFilesAs(ctx, ra, size, dir, EvalShape)
	if err != nil || base == nil {
		return nil, res, err
	}
	pkg := &Package{Package: base}

	inPackage := make(map[string]bool, len(base.Files))
	for _, f := range base.Files {
		inPackage[f.Path] = true
	}

	name := ManifestFile
	if !inPackage[name] && inPackage[ManifestFileAlt] {
		name = ManifestFileAlt
	}
	raw, err := read(base, name)
	if err != nil || len(raw) == 0 {
		res.Errorf("missing_manifest", "no %s at the package root", ManifestFile)
		return pkg, res, nil
	}
	if int64(len(raw)) > MaxManifest {
		res.Add(skill.SeverityError, "manifest_too_large",
			fmt.Sprintf("%s is %s; the maximum is %s", name,
				humanBytes(len(raw)), humanBytes(MaxManifest)), skill.At(name, 0))
		return pkg, res, nil
	}

	man, mres := ParseManifest(raw)
	res.Merge(mres)
	pkg.Eval = man

	// The dataset the manifest points at.
	if dp := man.DatasetPath(); dp != "" {
		if !inPackage[dp] {
			res.Add(skill.SeverityError, "dataset_missing",
				fmt.Sprintf("%s names %s, which is not in the package", name, dp),
				skill.At(name, 1),
				skill.Hint("file_path is relative to the package root."))
		} else if rc, err := open(base, dp); err != nil {
			res.Errorf("dataset_unreadable", "%s could not be opened: %v", dp, err)
		} else {
			rep := ReadDataset(rc, dp, man.Dataset.Features, inPackage, res)
			rc.Close()
			pkg.Dataset = &rep
		}
	}

	pkg.Rubrics = under(base.Files, dirOf(man.Eval.RubricsDirectory, RubricsDir))
	pkg.Metrics = under(base.Files, dirOf(man.Eval.MetricsDirectory, MetricsDir))
	checkPointers(pkg, name, res)

	if res.OK() {
		pkg.Digest = skill.TreeDigest(base.Files)
	}
	return pkg, res, nil
}

// checkPointers reports a manifest that names things the package does not hold,
// and code the package holds that the manifest never mentioned.
func checkPointers(pkg *Package, manifestName string, res *skill.Result) {
	m := pkg.Eval
	if m == nil {
		return
	}
	at := skill.At(manifestName, 1)

	if d := strings.TrimSpace(m.Eval.RubricsDirectory); d != "" && len(pkg.Rubrics) == 0 {
		res.Add(skill.SeverityError, "rubrics_missing",
			fmt.Sprintf("evaluation_type.rubrics_directory names %s, which holds nothing in this package", d),
			at)
	}
	// llm_as_a_judge without a rubric is a method with no instructions: the
	// judge is told to grade and never told against what.
	if m.Eval.Method == "llm_as_a_judge" && len(pkg.Rubrics) == 0 {
		res.Add(skill.SeverityWarn, "judge_without_rubric",
			"method is llm_as_a_judge and the package carries no rubric", at,
			skill.Hint("Put the grading prompt in rubrics/ so a judge is told what to grade against."))
	}
	// A metrics directory is executable code shipped inside a dataset. It is
	// allowed and it is never quiet: a reviewer reading a manifest would
	// otherwise not know it was there.
	if len(pkg.Metrics) > 0 {
		res.Add(skill.SeverityWarn, "package_carries_code",
			fmt.Sprintf("%d file(s) under %s/ are code that a runner executes",
				len(pkg.Metrics), MetricsDir), at,
			skill.Hint("Read them the way you would read a skill's scripts."))
	}
}

func dirOf(declared, fallback string) string {
	d := strings.TrimSpace(declared)
	if d == "" {
		d = fallback
	}
	d = strings.TrimPrefix(d, "./")
	return strings.Trim(path.Clean(d), "/")
}

func under(files []skill.FileEntry, dir string) []string {
	if dir == "" || dir == "." {
		return nil
	}
	var out []string
	for _, f := range files {
		if strings.HasPrefix(f.Path, dir+"/") {
			out = append(out, f.Path)
		}
	}
	return out
}

// read returns a file's bytes from wherever UnpackFiles put them: in memory
// when it was called without a directory, on disk when it was given one.
func read(pkg *skill.Package, rel string) ([]byte, error) {
	if pkg.Contents != nil {
		if b, ok := pkg.Contents[rel]; ok {
			return b, nil
		}
	}
	if pkg.Dir == "" {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(pkg.Dir, filepath.FromSlash(rel)))
}

// open streams a file, so a dataset is never held whole.
func open(pkg *skill.Package, rel string) (io.ReadCloser, error) {
	if pkg.Contents != nil {
		if b, ok := pkg.Contents[rel]; ok {
			return io.NopCloser(bytes.NewReader(b)), nil
		}
	}
	if pkg.Dir == "" {
		return nil, os.ErrNotExist
	}
	if strings.Contains(rel, "..") {
		return nil, fmt.Errorf("refusing path %q", rel)
	}
	return os.Open(filepath.Join(pkg.Dir, filepath.FromSlash(rel)))
}
