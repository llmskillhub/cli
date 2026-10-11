// Package evalpkg validates an eval package.
//
// The layout is the OpenEval Hub convention: a manifest.yaml naming a dataset
// and the columns inside it, a JSONL dataset, and optional rubrics and metrics
// directories beside them.
//
//	manifest.yaml
//	dataset.jsonl
//	rubrics/factual_accuracy.txt
//	metrics/execution_validator.py
//
// Deliberately a sibling of skillpkg rather than a mode inside it. The two
// share an envelope -- the same zip, the same bomb defence, the same path
// rules, the same scanner -- and nothing about what makes one valid. A skill is
// prose judged on whether a model can follow it; an eval is rows judged on
// whether they parse, whether they carry the columns the manifest declares, and
// whether anything in them executes.
//
// Everything structural comes from skillpkg and is not reimplemented here.
package evalpkg

// ManifestFile is the one required member of every eval package, and the reason
// a package is read by this validator rather than the other one.
const ManifestFile = "manifest.yaml"

// ManifestFileAlt is accepted because both spellings are in the wild and
// refusing one over a letter teaches nobody anything.
const ManifestFileAlt = "manifest.yml"

// RubricsDir and MetricsDir are where a manifest may point. Named here so the
// validator can say which directory it looked in when it found nothing.
const (
	RubricsDir = "rubrics"
	MetricsDir = "metrics"
)

// DatasetExt is the only dataset encoding accepted.
//
// Line-delimited because these files grow, and a line-delimited file streams,
// appends and diffs; a 40 MB JSON array has to be held whole to be read at all.
const DatasetExt = ".jsonl"

const (
	// MaxManifest matches skillpkg's cap on SKILL.md.
	MaxManifest = 64 << 10

	// MaxDatasetBytes is per file, and generous on purpose: a dataset is data,
	// and the archive caps in skillpkg already bound the whole package.
	MaxDatasetBytes = 32 << 20

	// MaxSampleBytes bounds one line. A sample far past this is usually a file
	// that should have been an attachment, or a JSONL that is really a JSON
	// array written on one line.
	MaxSampleBytes = 512 << 10

	// MaxSamples per package. High enough for a real benchmark, low enough that
	// a reviewer is being asked to spot-check rather than to read a corpus.
	MaxSamples = 50_000
)

// Methods the convention names. The word is a hint to whoever runs the eval
// rather than a schema we enforce: an unrecognised one is a warning, because
// this list will be out of date before the catalogue is.
var Methods = []string{"llm_as_a_judge", "exact_match", "contains", "tool_assertions", "compare_judge", "code_execution", "human",
	// review_decision: each sample names a whole package, and the expected
	// output is the decision a person made about it. The platform's review
	// evals use it (apps/api/internal/autoreview, docs/15).
	"review_decision"}
