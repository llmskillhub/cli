package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func observed(t *testing.T, c Catalogue, method, params string) Call {
	t.Helper()
	var got []Call
	s := &Server{Name: "t", Version: "0", Catalogue: c,
		Observe: func(_ context.Context, c Call) { got = append(got, c) }}
	s.Handle(context.Background(), method, json.RawMessage(params))
	if len(got) != 1 {
		t.Fatalf("Observe called %d times, want 1", len(got))
	}
	return got[0]
}

func TestObserveReportsASearchWithoutItsText(t *testing.T) {
	c := observed(t, &fakeCatalogue{skills: []Skill{
		{Owner: "a", Slug: "knit", Match: &Match{Confidence: "high"}},
		{Owner: "b", Slug: "purl"},
	}}, "tools/call", `{"name":"search_skills","arguments":{"query":"secret knitting plans"}}`)

	if c.Method != "tools/call" || c.Tool != "search_skills" {
		t.Errorf("method/tool = %q/%q", c.Method, c.Tool)
	}
	if c.Results != 2 || c.TopMatch != "high" {
		t.Errorf("results/top = %d/%q, want 2/high", c.Results, c.TopMatch)
	}
	// The privacy policy says search text isn't logged or stored.
	if strings.Contains(strings.ToLower(c.Skill+c.Tool+c.TopMatch+c.ClientName), "knitting") {
		t.Error("the search text reached telemetry")
	}
}

func TestObserveReportsAnEmptySearch(t *testing.T) {
	c := observed(t, &fakeCatalogue{meta: SearchMeta{Semantic: true}},
		"tools/call", `{"name":"search_skills","arguments":{"query":"nothing like this"}}`)
	if c.Results != 0 || c.TopMatch != "none" {
		t.Errorf("results/top = %d/%q, want 0/none", c.Results, c.TopMatch)
	}
}

// anySkill answers every lookup with the same skill, as the real catalogue
// does for a name that exists.
type anySkill struct{ fakeCatalogue }

func (anySkill) Skill(_ context.Context, owner, slug string) (*Skill, error) {
	return &Skill{Owner: owner, Slug: slug}, nil
}

func TestObserveNamesTheSkillAndTheClient(t *testing.T) {
	c := observed(t, &anySkill{}, "tools/call",
		`{"name":"install_command","arguments":{"name":"sinhasheshant/citable-markdown"}}`)
	if c.Skill != "sinhasheshant/citable-markdown" || c.Results != -1 {
		t.Errorf("skill/results = %q/%d", c.Skill, c.Results)
	}
	c = observed(t, &fakeCatalogue{}, "initialize",
		`{"protocolVersion":"2025-06-18","clientInfo":{"name":"claude-code","version":"2.1.296"}}`)
	if c.ClientName != "claude-code" || c.ClientVersion != "2.1.296" {
		t.Errorf("client = %q %q", c.ClientName, c.ClientVersion)
	}
}

// A client can send any method or tool name; telemetry must not grow a new
// value for each one.
func TestObserveFoldsUnknownNames(t *testing.T) {
	if c := observed(t, &fakeCatalogue{}, "made/up", `{}`); c.Method != "other" {
		t.Errorf("method = %q, want other", c.Method)
	}
	c := observed(t, &fakeCatalogue{}, "tools/call", `{"name":"drop_tables","arguments":{}}`)
	if c.Tool != "unknown" || !c.IsError {
		t.Errorf("tool/isError = %q/%v, want unknown/true", c.Tool, c.IsError)
	}
}

type missing struct{ fakeCatalogue }

func (missing) Skill(context.Context, string, string) (*Skill, error) {
	return nil, NotFound("no published skill called doc-coauthoring")
}

// Not found is recorded as such, and not as an error.
func TestObserveRecordsNotFoundApartFromErrors(t *testing.T) {
	c := observed(t, &missing{}, "tools/call", `{"name":"get_skill","arguments":{"name":"doc-coauthoring"}}`)
	if !c.NotFound || c.IsError {
		t.Errorf("notFound/isError = %v/%v, want true/false", c.NotFound, c.IsError)
	}
}

// install_command names the flag for the agent asking, so a skill lands where
// that agent loads skills -- and stays plain for claude, the default.
func TestInstallCommandIsForTheAgentAsking(t *testing.T) {
	s := &Server{Name: "t", Version: "0", Catalogue: &anySkill{}, InstallCommand: "llmsh install"}
	cases := map[string]string{
		`{"name":"a/x","version":"1.0.0"}`:                   "llmsh install a/x@1.0.0\n",
		`{"name":"a/x","version":"1.0.0","agent":"claude"}`:  "llmsh install a/x@1.0.0\n",
		`{"name":"a/x","version":"1.0.0","agent":"cursor"}`:  "llmsh install a/x@1.0.0 --for cursor\n",
		`{"name":"a/x","version":"1.0.0","agent":"Codex"}`:   "llmsh install a/x@1.0.0 --for codex\n",
		`{"name":"a/x","version":"1.0.0","agent":"notepad"}`: "llmsh install a/x@1.0.0\n",
	}
	for args, want := range cases {
		out, err := s.call(context.Background(), "install_command", json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "  "+want) {
			t.Errorf("%s:\n got %q\nwant a line %q", args, out, want)
		}
	}
	out, _ := s.call(context.Background(), "install_command", json.RawMessage(`{"name":"a/x","agent":"cursor"}`))
	if !strings.Contains(out, ".cursor/skills/") {
		t.Errorf("cursor install doesn't say where it lands: %s", out)
	}
}
