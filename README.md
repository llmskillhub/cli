# llmsh

Publish and install versioned skills for AI agents, from
[LLM SkillHub](https://llmskillhub.com).

A skill is a folder of instructions, scripts and references that an agent
loads. `llmsh` packages one, checks it against the
[Agent Skills spec](https://agentskills.io/specification), submits it for
review, and installs published ones with their tree digest verified.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/llmskillhub/cli/main/install.sh | sh
```

```sh
npm install -g llmskillhub     # the command is still llmsh
```

```sh
go install github.com/llmskillhub/cli/cmd/llmsh@latest
```

Or take a binary from [releases](https://github.com/llmskillhub/cli/releases).
Every release publishes `SHA256SUMS`; the installer checks it and refuses to
install a file that does not match.

## Use

```sh
llmsh install anthropics/mcp-builder     # download, verify, unpack
llmsh install name@1.2.0                 # an exact version
llmsh install name -for cursor           # put it where Cursor will read it
llmsh validate ./my-skill                # check it locally, storing nothing
llmsh publish ./my-skill                 # submit it for review
llmsh status owner/skill                 # versions and review state
llmsh login                              # store an access token
```

`llmsh` reads the repository when publishing: it will not ship uncommitted work
by accident, and it records the commit a version came from.

## Private skills

A skill can live in your own space instead of the catalogue:

```sh
llmsh publish ./my-skill --private
```

It is not reviewed, it is not listed, and it is available to you the moment it
uploads. Search and `llmsh install` find it as normal, because your token says
who you are; to anybody else it does not exist.

Pass `--private` on every version, not only the first. Visibility belongs to the
skill rather than to a release, and the server refuses a version that disagrees
with its skill — publishing a public version into a private skill would take a
listed package out of the catalogue as a side effect of shipping an update, and
the reverse would put unreviewed work on the front page. Forgetting the flag is
a refusal that names both sides.

A private space holds 5 MB unpacked, counted across every version you keep
rather than only the newest — ten revisions of a 500 KB skill fill it. Versions
of a private skill can be deleted to make room, which is the one place anything
here is deletable; a published public version is permanent because people depend
on it.

Collaborators are managed on the website: add somebody to your space, then choose
which private skills they can see, or all of them.

## Any agent, not just Claude

A skill is a folder of instructions, which is not a Claude idea. Where those
instructions have to live to be read is different for every agent, so `-for`
puts them where yours looks.

| `-for` | where the skill goes | how the agent hears about it |
|---|---|---|
| `claude` | `.claude/skills/` | reads the folder, picks by description |
| `opencode` | `.opencode/skills/` | the same |
| `cursor` | `.cursor/skills/` | a rule in `.cursor/rules/` |
| `codex` | `.agents/skills/` | a block in `AGENTS.md` |
| `gemini` | `.agents/skills/` | a block in `GEMINI.md` |

Codex and Gemini CLI read one project file rather than a folder, so the skill's
instructions are written into it between comment markers naming the skill.
Everything outside those markers is yours and is never touched: installing again
replaces only that block, and a file whose markers look half-written is refused
rather than repaired.

Those two share `.agents/skills`, so installing the same skill for the second of
them re-uses the copy that is there once its digest matches, rather than
downloading it twice. opencode reads `.claude/skills` and `.agents/skills` as
well as its own directory, so anything installed for another agent is already
visible to it.

## Configuration

| | |
|---|---|
| `LLMSH_API` | API address, default `https://api.llmskillhub.com` |
| `LLMSH_INGEST` | upload address, default the same |
| `LLMSH_TOKEN` | a token for CI, instead of the stored one |
| `LLMSH_EVALS` | `1` to work with evals as well as skills |

The stored token lives in your user config directory, `0600`, and is the only
thing written there.

## Evals

An eval is the other kind of package: a dataset of cases with a manifest saying
what they measure and how to judge an answer. Where a skill teaches an agent to
do something, an eval checks whether it did.

They are behind a switch, because a catalogue has to accept them before the
command can do anything useful:

```sh
export LLMSH_EVALS=1
```

Publishing is the same command. A directory with a `manifest.yaml` publishes as
an eval; one with a `SKILL.md` publishes as a skill. Nothing to remember and
nothing to pass:

```sh
llmsh publish ./my-eval      # reads the manifest, counts the rows, submits it
llmsh eval get owner/name    # fetch one back, into ./evals
```

`publish` reports what the package turned out to contain — how many samples,
how many carry an expected answer, how many rubrics — because those are numbers
you can check against what you believe you wrote. It also says plainly when an
eval executes anything: a sample carrying a setup script, a sandbox, or a file
fetched from a URL is content a reviewer will ask about, and better to see it
before you submit than after.

`eval get` unpacks to `./evals` rather than into an agent's skills directory.
An agent's directories are for instructions, and dropping two hundred test
cases into one would have it read them as guidance.

If the switch is off, or the catalogue has not enabled evals, you get told
which — not a 404 that looks like a fault in your package.

## What happens when you publish

Nothing is public straight away. A version is unpacked, validated against the
spec, and scanned for credentials and prompt injection; a live credential
blocks it outright, and everything else is flagged for a person who reads it
before it is listed. Published versions are immutable — the tree digest is the
version's identity, and `llmsh install` checks it before writing a file.

## This repository

Generated from the private monorepo that also holds the services. The CLI is
the part strangers are asked to run, so it is the part that has to be readable.
Issues and pull requests are welcome here; changes land upstream and are
synced back.
