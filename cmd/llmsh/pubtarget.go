package main

import (
	"fmt"

	"github.com/llmskillhub/cli/internal/workdir"
)

/*
Which skill a command in a working copy is about.

A bare name can only ever mean "mine", and for somebody who clones a skill they
collaborate on, "mine" is the wrong answer in the quietest possible way. They
clone alice/pdf-tools, edit it, run the command the clone told them to run, and
publish a version into a pdf-tools of their own -- a different skill, possibly
one they already had, with no error anywhere and a success message saying the
work is theirs now.

Permissions cannot disambiguate this: somebody with write on three people's
pdf-tools has the right to publish to any of them. Nor can the name, which is
the same in every case. The only thing that knows is the directory, because the
clone wrote it down.

So the working copy decides, the way .git/config decides for git: the directory
you are standing in carries the destination, and the name stops being
load-bearing. An argument naming owner/slug still wins, because somebody who
says where they mean has said it.
*/
type pubTarget struct {
	// Owner is empty when the answer is "whoever is publishing", which is what
	// a directory with no origin means: a skill you wrote, not one you fetched.
	Owner string
	// From is where Owner came from, for messages. Empty when nothing was
	// inherited.
	From string
}

// targetFor resolves the owner a command should act on.
//
// explicit wins; then --fork, which is how somebody says "my own copy" of
// something they cloned; then the working copy; then nothing, meaning the
// caller's own namespace.
//
// slug is the package's own name. When it no longer matches the origin, the
// origin has stopped describing this directory -- renaming is how a fork is
// made by hand -- so it is ignored rather than used to publish under a name
// its owner never chose.
func targetFor(dir, slug, explicit string, fork bool) (pubTarget, error) {
	if explicit != "" {
		return pubTarget{Owner: explicit, From: "the name you gave"}, nil
	}
	if fork {
		return pubTarget{}, nil
	}
	o, err := workdir.Read(dir)
	if err != nil || o == nil || o.Owner == "" {
		// A directory that is not a working copy is not an error here. It is
		// the ordinary case: somebody wrote a skill and is publishing it.
		return pubTarget{}, nil
	}
	if slug != "" && o.Slug != "" && slug != o.Slug {
		return pubTarget{}, nil
	}
	return pubTarget{Owner: o.Owner, From: "this working copy"}, nil
}

// renamedFrom reports the origin slug when the package has been renamed away
// from it, so a caller can say why it stopped following the origin.
func renamedFrom(dir, slug string) string {
	o, err := workdir.Read(dir)
	if err != nil || o == nil || o.Slug == "" || slug == "" || o.Slug == slug {
		return ""
	}
	return fmt.Sprintf("%s/%s", o.Owner, o.Slug)
}
