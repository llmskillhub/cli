package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeCatalogue answers searches however a test needs, and refuses everything
// else -- these tests are about what the search tool says, not about storage.
type fakeCatalogue struct {
	skills []Skill
	meta   SearchMeta
}

func (f *fakeCatalogue) Search(context.Context, string, string, int) ([]Skill, SearchMeta, error) {
	return f.skills, f.meta, nil
}
func (f *fakeCatalogue) Skill(context.Context, string, string) (*Skill, error)       { return nil, nil }
func (f *fakeCatalogue) Versions(context.Context, string, string) ([]Version, error) { return nil, nil }
func (f *fakeCatalogue) File(context.Context, string, string, string, string) (*File, error) {
	return nil, nil
}
func (f *fakeCatalogue) Categories(context.Context) ([]Category, error) { return nil, nil }
func (f *fakeCatalogue) WebURL(owner, slug string) string               { return "" }

func searchWith(t *testing.T, c *fakeCatalogue) string {
	t.Helper()
	s := &Server{Name: "test", Version: "0", Catalogue: c}
	out, err := s.call(context.Background(), "search_skills",
		json.RawMessage(`{"query":"knitting patterns","limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var oneSkill = []Skill{{Owner: "someone", Slug: "a-skill", Description: "Does a thing."}}

// A model reading five names cannot tell things that match from things that
// are merely nearby, and the difference decides whether it recommends one or
// keeps looking.
func TestRelaxedResultsSayTheyAreNearMisses(t *testing.T) {
	out := searchWith(t, &fakeCatalogue{skills: oneSkill, meta: SearchMeta{Semantic: true, Relaxed: true}})
	if !strings.Contains(out, "related to") || !strings.Contains(out, "near misses") {
		t.Errorf("a relaxed search did not say so:\n%s", out)
	}
	first := strings.SplitN(out, "\n", 2)[0]
	if !strings.Contains(first, "near misses") {
		t.Errorf("the caveat is not on the first line, where it is read before the names:\n%s", first)
	}
}

func TestMatchingResultsDoNotApologise(t *testing.T) {
	out := searchWith(t, &fakeCatalogue{skills: oneSkill, meta: SearchMeta{Semantic: true}})
	for _, word := range []string{"related to", "near misses"} {
		if strings.Contains(out, word) {
			t.Errorf("a clean match was hedged with %q:\n%s", word, out)
		}
	}
}

// Empty is where the advice differs. Searched by meaning, a rephrasing will
// not find what this one missed, and a model told only "no match" will try
// three more wordings before concluding the same thing.
func TestEmptyResultsSayWhetherRephrasingCouldHelp(t *testing.T) {
	semantic := searchWith(t, &fakeCatalogue{meta: SearchMeta{Semantic: true}})
	if !strings.Contains(semantic, "rephrasing is unlikely") {
		t.Errorf("a semantic miss did not discourage rephrasing:\n%s", semantic)
	}

	// A single word is matched literally by design, so the advice is to add
	// words -- not to reword, and not that anything is broken.
	oneWord := &Server{Name: "t", Version: "0", Catalogue: &fakeCatalogue{}}
	single, err := oneWord.call(context.Background(), "search_skills", json.RawMessage(`{"query":"knitting"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(single, "unavailable") {
		t.Errorf("a one-word miss blamed an outage that was not happening:\n%s", single)
	}
	if !strings.Contains(single, "a few words") {
		t.Errorf("a one-word miss did not say what actually helps:\n%s", single)
	}

	wordsOnly := searchWith(t, &fakeCatalogue{meta: SearchMeta{}})
	if !strings.Contains(wordsOnly, "different wording may work") {
		t.Errorf("a keyword-only miss did not suggest rephrasing:\n%s", wordsOnly)
	}
	if strings.Contains(wordsOnly, "rephrasing is unlikely") {
		t.Errorf("a keyword-only miss gave the semantic advice:\n%s", wordsOnly)
	}
}

// The page being full is the only hint that there is more to ask for.
func TestAFullPageSaysThereMayBeMore(t *testing.T) {
	five := make([]Skill, 5)
	for i := range five {
		five[i] = oneSkill[0]
	}
	out := searchWith(t, &fakeCatalogue{skills: five, meta: SearchMeta{Semantic: true}})
	if !strings.Contains(out, "raise limit") {
		t.Errorf("a full page did not say more were available:\n%s", out)
	}
	fewer := searchWith(t, &fakeCatalogue{skills: oneSkill, meta: SearchMeta{Semantic: true}})
	if strings.Contains(fewer, "raise limit") {
		t.Errorf("a partial page claimed there were more:\n%s", fewer)
	}
}

func TestSummariseKeepsTheOpeningAndCutsCleanly(t *testing.T) {
	short := "Does a thing."
	if got := summarise(short); got != short {
		t.Errorf("a short description was altered: %q", got)
	}

	long := strings.Repeat("word ", 200)
	got := summarise(long)
	if n := len([]rune(got)); n > summaryChars+1 {
		t.Errorf("summarise returned %d runes, over the %d budget", n, summaryChars)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a shortened description does not say it was shortened: %q", got)
	}
	if strings.Contains(strings.TrimSuffix(got, "…"), "wor…") || strings.HasSuffix(strings.TrimSuffix(got, "…"), "wor") {
		t.Errorf("cut mid-word: %q", got)
	}

	// A real one from the catalogue, to check the opening survives.
	real := "Professional deep research report generation — multi-agent collaboration with parallel chapter writing, automatic latest-data targeting, multilingual output, and built-in quality checks, plus a great many further features nobody reads in a list."
	if s := summarise(real); !strings.HasPrefix(s, "Professional deep research report generation") {
		t.Errorf("the opening was lost: %q", s)
	}

	// One unbroken token has no good cut point; it must still be bounded.
	blob := strings.Repeat("x", 900)
	if n := len([]rune(summarise(blob))); n > summaryChars+1 {
		t.Errorf("an unbroken description was not bounded: %d runes", n)
	}

	// Multi-byte characters at the cut point must not be halved.
	dashes := strings.Repeat("a — b ", 100)
	if out := summarise(dashes); !utf8.ValidString(out) {
		t.Errorf("summarise produced invalid UTF-8: %q", out)
	}
}

// The point of the default is that a name alone is enough to read the
// instructions, without a get_skill call first to learn the file is there.
func TestReadingAFileDefaultsToSkillMD(t *testing.T) {
	s := &Server{Name: "test", Version: "0", Catalogue: &fakeCatalogue{}}
	_, err := s.call(context.Background(), "read_skill_file", json.RawMessage(`{"name":"a-skill"}`))
	if err != nil && strings.Contains(err.Error(), "path") {
		t.Errorf("a path is still required: %v", err)
	}
	for _, tl := range Tools {
		if tl["name"] != "read_skill_file" {
			continue
		}
		in := tl["inputSchema"].(map[string]any)
		if req, _ := in["required"].([]string); len(req) > 0 {
			t.Errorf("read_skill_file still requires %v", req)
		}
	}
}

// Each result says how far to trust it, beside the evidence, so a model can
// recommend the strong ones and read the weak ones before saying anything.
func TestResultsCarryTheirMatchLine(t *testing.T) {
	skills := []Skill{
		{Owner: "someone", Slug: "named", Description: "Does a thing.",
			Match: &Match{Confidence: "high", By: []string{"name", "words", "meaning"}, Words: "2/2"}},
		{Owner: "someone", Slug: "nearby", Description: "Does another thing.",
			Match: &Match{Confidence: "low", By: []string{"meaning"}}},
	}
	out := searchWith(t, &fakeCatalogue{skills: skills, meta: SearchMeta{Semantic: true}})
	for _, want := range []string{
		"match: high (name, words 2/2, meaning)",
		"match: low (meaning)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// A catalogue too old to label its results says nothing, rather than a match
// line with no confidence in it.
func TestNoMatchLineWithoutALabel(t *testing.T) {
	out := searchWith(t, &fakeCatalogue{skills: oneSkill, meta: SearchMeta{Semantic: true}})
	if strings.Contains(out, "match:") {
		t.Errorf("an unlabelled result grew a match line:\n%s", out)
	}
}
