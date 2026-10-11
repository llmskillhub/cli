// Package client talks to the alphaQ services.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Client struct {
	API    string
	Ingest string
	Token  string
	HTTP   *http.Client
	// CachePath is where an exchanged access token is kept between commands.
	// Empty keeps it in memory only. See access.go.
	CachePath string

	mu        sync.Mutex
	access    string
	accessExp time.Time
	direct    bool // the API has no exchange; send the personal token itself
}

func New(api, ingest, token string) *Client {
	return &Client{API: api, Ingest: ingest, Token: token,
		HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Problem is the API's error shape. Rendered rather than swallowed, because the
// server's hint is usually the whole answer.
type Problem struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Hint   string `json:"hint"`
}

func (p *Problem) Error() string {
	msg := p.Detail
	if msg == "" {
		msg = p.Title
	}
	if p.Hint != "" {
		msg += "\n  " + p.Hint
	}
	return msg
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")

	resp, err := c.send(c.HTTP, req)
	if err != nil {
		// A transport failure names the address, because "connection refused"
		// without one sends people to check the wrong service.
		return fmt.Errorf("could not reach %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	if resp.StatusCode == 401 {
		return fmt.Errorf("not signed in, or the token was revoked\n  Run: llmsh login")
	}
	if resp.StatusCode >= 400 {
		var p Problem
		if json.Unmarshal(body, &p) == nil && (p.Code != "" || p.Detail != "") {
			return &p
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *Client) getJSON(path string, out any) error {
	req, err := http.NewRequest("GET", c.API+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

type Me struct {
	ID      string `json:"id"`
	Handle  string `json:"handle"`
	IsAdmin bool   `json:"is_admin"`
}

func (c *Client) Me() (*Me, error) {
	var m Me
	return &m, c.getJSON("/v1/me", &m)
}

// Violation mirrors the server's, so the CLI prints the same codes a reviewer
// will quote back.
type Violation struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
	Hint     string `json:"hint"`
}

type UploadResponse struct {
	OK          bool        `json:"ok"`
	DryRun      bool        `json:"dry_run"`
	Slug        string      `json:"slug"`
	Version     string      `json:"version"`
	VersionID   string      `json:"version_id"`
	ReviewState string      `json:"review_state"`
	Digest      string      `json:"digest"`
	ShortDigest string      `json:"short_digest"`
	Size        int64       `json:"size"`
	Message     string      `json:"message"`
	Violations  []Violation `json:"violations"`
	Scores      struct {
		Description struct {
			Total int `json:"total"`
		} `json:"description"`
		Completeness struct {
			Total int `json:"total"`
		} `json:"completeness"`
	} `json:"scores"`
}

// Publish uploads an archive to ingest, which validates it and asks api to
// record it. The CLI never talks to api for publishing: that boundary is the
// reason ingest exists.
//
// dryRun selects a different ENDPOINT, not a different parameter. /v1/validate
// inflates and checks the archive and stores nothing; /v1/upload stores. A flag
// that only changed what the CLI printed -- which is what this was at first --
// promises one thing and does the other, which is worse than not offering it.
// Publish sends an archive. kind is "skill" or "eval"; empty means skill,
// which is what every caller meant before there was a second kind.
//
// The kind travels in the query string rather than being inferred from the
// archive, because the server settles it when it authorises the publish --
// before a byte is stored -- and reads it back from the intent at commit. A
// package that validated as one kind and recorded as another is the thing that
// ordering prevents.
// PublishOptions is what a publish is, beyond its bytes.
//
// A struct rather than five positional arguments, because the last two would be
// adjacent bools: Publish(..., dryRun, private) and Publish(..., private,
// dryRun) both compile, and one of them uploads somebody's private work to the
// public catalogue while telling them it was a dry run.
type PublishOptions struct {
	Slug string
	// Owner is whose space this goes into, when that is not the person
	// running the command. A collaborator publishing into somebody else's
	// skill names them; empty means your own space.
	Owner   string
	Version string
	Kind    string
	// Private keeps this out of the catalogue. Settled at upload because the
	// server settles it when the publish is authorised -- it will not take it
	// at commit, and a version that disagrees with its skill is refused.
	Private bool
	DryRun  bool
}

func (c *Client) Publish(opt PublishOptions, archive []byte) (*UploadResponse, error) {
	slug, version, kind := opt.Slug, opt.Version, opt.Kind
	dryRun := opt.DryRun
	path := "/v1/upload"
	q := url.Values{"slug": {slug}}
	if opt.Owner != "" {
		q.Set("owner", opt.Owner)
	}
	if version != "" {
		q.Set("version", version)
	}
	if kind != "" && kind != "skill" {
		q.Set("kind", kind)
	}
	if opt.Private {
		q.Set("private", "1")
	}
	if dryRun {
		// validate takes no slug or version: it reports on the bytes alone.
		// The kind still goes, because the two kinds are read by different
		// validators and a dry run that used the wrong one would report on
		// rules this package was never going to be judged by.
		path, q = "/v1/validate", url.Values{}
		if kind != "" && kind != "skill" {
			q.Set("kind", kind)
		}
		// private is deliberately not sent on a validate. Validation reads the
		// bytes and says whether they are a well-formed package, which is the
		// same question either way -- and /v1/validate stores nothing, so there
		// is no visibility for it to be about.
	}
	req, err := http.NewRequest("POST", c.Ingest+path+"?"+q.Encode(), bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/zip")
	var out UploadResponse
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type Version struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Digest      string `json:"digest"`
	ShortDigest string `json:"short_digest"`
	Size        int64  `json:"size"`
	FileCount   int    `json:"file_count"`
	PublishedAt string `json:"published_at"`
	ReviewState string `json:"review_state"`
	ReviewNotes string `json:"review_notes"`
	Yanked      bool   `json:"yanked"`
}

func (c *Client) Versions(owner, slug string) ([]Version, error) {
	var out struct {
		Versions []Version `json:"versions"`
	}
	err := c.getJSON(fmt.Sprintf("/v1/skills/%s/%s/versions",
		url.PathEscape(owner), url.PathEscape(slug)), &out)
	return out.Versions, err
}

type ReviewDecision struct {
	Version     string   `json:"version"`
	ReviewState string   `json:"review_state"`
	ReasonText  []string `json:"reason_text"`
	Notes       string   `json:"notes"`
	Reviewer    string   `json:"reviewer"`
}

type Skill struct {
	Owner  string `json:"owner"`
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

type Profile struct {
	Skills   []Skill                     `json:"skills"`
	Versions map[string][]Version        `json:"versions"`
	History  map[string][]ReviewDecision `json:"review_history"`
}

func (c *Client) Profile(handle string) (*Profile, error) {
	var p Profile
	return &p, c.getJSON("/v1/users/"+url.PathEscape(handle), &p)
}

// FileContent is one file of a published version, for diffing against local.
type FileContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	IsText  bool   `json:"is_text"`
	Size    int64  `json:"size"`
}

func (c *Client) File(owner, slug, version, path string) (*FileContent, error) {
	var f FileContent
	q := url.Values{"version": {version}, "path": {path}}
	return &f, c.getJSON(fmt.Sprintf("/v1/skills/%s/%s/file?%s",
		url.PathEscape(owner), url.PathEscape(slug), q.Encode()), &f)
}

type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (c *Client) Files(owner, slug, version string) ([]FileEntry, error) {
	var out struct {
		Files []FileEntry `json:"files"`
	}
	q := url.Values{"version": {version}}
	err := c.getJSON(fmt.Sprintf("/v1/skills/%s/%s/files?%s",
		url.PathEscape(owner), url.PathEscape(slug), q.Encode()), &out)
	return out.Files, err
}

// Download fetches a version's archive and the digest it must hash to.
//
// Kept for the callers that only ever handle skills -- clone works on a
// SKILL.md-shaped working copy -- so they are not made to accept a kind they
// would have to ignore.
func (c *Client) Download(owner, slug, version string) (archive []byte, digest string, err error) {
	archive, digest, _, err = c.downloadPackage(owner, slug, version)
	return archive, digest, err
}

// DownloadPackage is Download plus what the server says the package is:
// "skill" or "eval", or empty from a server too old to say.
func (c *Client) DownloadPackage(owner, slug, version string) (archive []byte, digest, kind string, err error) {
	return c.downloadPackage(owner, slug, version)
}

// downloadPackage does the fetching for both.
//
// The API answers with a redirect to a short-lived URL on the object store, so
// the bytes never pass through it. The digest comes back in a header on the
// redirect itself, which is the response the API signed off on -- reading it
// from the final hop would mean trusting whatever served the bytes.
func (c *Client) downloadPackage(owner, slug, version string) (archive []byte, digest, kind string, err error) {
	q := url.Values{}
	if version != "" {
		q.Set("version", version)
	}
	u := fmt.Sprintf("%s/v1/skills/%s/%s/download?%s",
		c.API, url.PathEscape(owner), url.PathEscape(slug), q.Encode())

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, "", "", err
	}

	// Stop at the redirect so the headers the API set are readable, and so the
	// Authorization header is not replayed to the object store, which neither
	// needs it nor should see it.
	noFollow := &http.Client{
		Timeout: c.HTTP.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.send(noFollow, req)
	if err != nil {
		return nil, "", "", fmt.Errorf("could not reach %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == 404:
		return nil, "", "", fmt.Errorf("no such skill or version, or it is not public yet")
	case resp.StatusCode >= 400:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var p Problem
		if json.Unmarshal(body, &p) == nil && p.Detail != "" {
			return nil, "", "", &p
		}
		return nil, "", "", fmt.Errorf("%s", resp.Status)
	}

	digest = resp.Header.Get("X-Skill-Digest")
	// What it is, from the same response as the digest and for the same
	// reason: this is the answer the API signed off on, not one inferred from
	// the bytes it is about. Empty from a server that predates the header,
	// which every caller treats as a skill -- the only kind there was.
	kind = resp.Header.Get("X-Skill-Kind")
	loc := resp.Header.Get("Location")
	if loc == "" {
		// A deployment serving bytes directly rather than redirecting.
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		return b, digest, kind, err
	}

	get, err := http.NewRequest("GET", loc, nil)
	if err != nil {
		return nil, "", "", err
	}
	blob, err := c.HTTP.Do(get)
	if err != nil {
		return nil, "", "", fmt.Errorf("could not fetch the archive: %w", err)
	}
	defer blob.Body.Close()
	if blob.StatusCode != 200 {
		return nil, "", "", fmt.Errorf("the download link returned %s", blob.Status)
	}
	b, err := io.ReadAll(io.LimitReader(blob.Body, 64<<20))
	return b, digest, kind, err
}

// SkillSummary is a catalogue entry as the list and detail endpoints return it.
type SkillSummary struct {
	Owner string `json:"owner"`
	Slug  string `json:"slug"`
	// Kind is "skill" or "eval". Sent on every listing row by the API; carried
	// here so the agent-facing tools can say which one a result is.
	Kind        string   `json:"kind"`
	FullName    string   `json:"full_name"`
	Description string   `json:"description"`
	Categories  []string `json:"categories"`
	Keywords    []string `json:"keywords"`
	License     string   `json:"license,omitempty"`
	Repository  string   `json:"repository,omitempty"`
	Downloads   int      `json:"downloads"`
	UpdatedAt   string   `json:"updated_at"`
	Latest      *Version `json:"latest_version,omitempty"`
	// Match is present on search results: why the catalogue returned this
	// one and how far to trust it.
	Match *Match `json:"match,omitempty"`
}

// Match is the API's confidence label for one search result.
type Match struct {
	Confidence string   `json:"confidence"`
	By         []string `json:"by"`
	Words      string   `json:"words,omitempty"`
}

// Search asks the catalogue, using the same endpoint and the same ranking the
// website uses. There is deliberately no second search implementation here:
// two rankings over one catalogue would disagree, and the one nobody was
// looking at would be the one an agent saw.
// SearchResult is how the catalogue answered, beside what it answered with.
//
// Relaxed and Semantic are the difference between "these match" and "these are
// related", which is the one thing a caller cannot work out from the list
// itself -- and the MCP server passes it through to an agent for exactly that
// reason.
type SearchResult struct {
	Skills   []SkillSummary
	Count    int
	Relaxed  bool
	Semantic bool
	HasMore  bool
}

func (c *Client) Search(query, category, sort string, limit int) (SearchResult, error) {
	q := url.Values{}
	if query != "" {
		q.Set("q", query)
	}
	if category != "" {
		q.Set("category", category)
	}
	if sort != "" {
		q.Set("sort", sort)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Skills   []SkillSummary `json:"skills"`
		Count    int            `json:"count"`
		Relaxed  bool           `json:"relaxed"`
		Semantic bool           `json:"semantic"`
		HasMore  bool           `json:"has_more"`
	}
	if err := c.getJSON("/v1/skills?"+q.Encode(), &out); err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Skills: out.Skills, Count: out.Count,
		Relaxed: out.Relaxed, Semantic: out.Semantic, HasMore: out.HasMore}, nil
}

// Skill fetches one entry.
func (c *Client) Skill(owner, slug string) (*SkillSummary, error) {
	var out SkillSummary
	if err := c.getJSON(fmt.Sprintf("/v1/skills/%s/%s",
		url.PathEscape(owner), url.PathEscape(slug)), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// postJSON sends a JSON body and decodes the JSON reply. A nil body sends an
// empty object, which the API's decoder accepts.
func (c *Client) postJSON(path string, body, out any) error {
	var buf bytes.Buffer
	if body == nil {
		body = map[string]any{}
	}
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequest("POST", c.API+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// AttachResult is one target skill's outcome from an attach or detach.
type AttachResult struct {
	Skill string `json:"skill"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// AttachEval attaches one eval to one or more of the caller's skills. skills
// are "owner/slug" references. See docs/14.
func (c *Client) AttachEval(evalOwner, evalSlug string, skills []string) ([]AttachResult, error) {
	return c.attachOrDetach("attach", evalOwner, evalSlug, skills)
}

// DetachEval is the reverse.
func (c *Client) DetachEval(evalOwner, evalSlug string, skills []string) ([]AttachResult, error) {
	return c.attachOrDetach("detach", evalOwner, evalSlug, skills)
}

func (c *Client) attachOrDetach(verb, evalOwner, evalSlug string, skills []string) ([]AttachResult, error) {
	var out struct {
		Results []AttachResult `json:"results"`
	}
	path := fmt.Sprintf("/v1/evals/%s/%s/%s",
		url.PathEscape(evalOwner), url.PathEscape(evalSlug), verb)
	if err := c.postJSON(path, map[string]any{"skills": skills}, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// AttachedEval is one eval attached to a skill, as the list surfaces it.
type AttachedEval struct {
	FullName string `json:"full_name"`
	Slug     string `json:"slug"`
	Owner    string `json:"owner"`
	Source   string `json:"source"`
	Private  bool   `json:"private"`
}

// SkillEvals lists the evals attached to one skill, as the caller may see them.
func (c *Client) SkillEvals(owner, slug string) ([]AttachedEval, error) {
	var out struct {
		Evals []AttachedEval `json:"evals"`
	}
	if err := c.getJSON(fmt.Sprintf("/v1/skills/%s/%s/evals",
		url.PathEscape(owner), url.PathEscape(slug)), &out); err != nil {
		return nil, err
	}
	return out.Evals, nil
}

// EvalRun is one run, as the CLI prints it.
type EvalRun struct {
	ID string `json:"id"`
	// Eval is the eval's owner/slug.
	Eval       string         `json:"eval,omitempty"`
	Trigger    string         `json:"trigger,omitempty"`
	Status     string         `json:"status"`
	Visibility string         `json:"visibility"`
	Score      *float64       `json:"score,omitempty"`
	MaxScore   *float64       `json:"max_score,omitempty"`
	Tallies    map[string]int `json:"tallies,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	Error      string         `json:"error,omitempty"`
	Model      string         `json:"model,omitempty"`
	Method     string         `json:"method,omitempty"`
	// Integrity is what the platform found about the eval itself; Counted
	// is false when a serious finding keeps the score out of the public one.
	Integrity []IntegrityFlag `json:"integrity,omitempty"`
	Counted   *bool           `json:"counted,omitempty"`
	CreatedAt string          `json:"created_at,omitempty"`
}

// IntegrityFlag is one finding about an author-written eval.
type IntegrityFlag struct {
	Flag    string `json:"flag"`
	Serious bool   `json:"serious"`
	Detail  string `json:"detail,omitempty"`
}

// RunEval queues a run of one eval against one of the caller's skills, both
// "owner/slug". Returns the run id to poll. The client header records that a
// CLI started it, which the server stores as the run's trigger.
func (c *Client) RunEval(evalRef, skillRef string) (*EvalRun, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]string{"eval": evalRef, "skill": skillRef}); err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.API+"/v1/eval-runs", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LLMSH-Client", "cli")
	// The 202 names the run as run_id, not id: it is a receipt for a run,
	// not the run itself.
	var out struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	if out.RunID == "" {
		return nil, fmt.Errorf("the server queued the run but did not say its id")
	}
	return &EvalRun{ID: out.RunID, Status: out.Status}, nil
}

// EvalRunStatus polls one run.
func (c *Client) EvalRunStatus(id string) (*EvalRun, error) {
	var out EvalRun
	if err := c.getJSON("/v1/eval-runs/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SkillRuns lists a skill's runs.
func (c *Client) SkillRuns(owner, slug string) ([]EvalRun, error) {
	var out struct {
		Runs []EvalRun `json:"runs"`
	}
	if err := c.getJSON(fmt.Sprintf("/v1/skills/%s/%s/runs",
		url.PathEscape(owner), url.PathEscape(slug)), &out); err != nil {
		return nil, err
	}
	return out.Runs, nil
}

// Category is one entry in the catalogue's fixed vocabulary.
type Category struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Blurb string `json:"blurb"`
	Count int    `json:"count"`
}

func (c *Client) Categories() ([]Category, error) {
	var out struct {
		Categories []Category `json:"categories"`
	}
	if err := c.getJSON("/v1/categories", &out); err != nil {
		return nil, err
	}
	return out.Categories, nil
}
