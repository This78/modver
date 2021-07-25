// Command modver compares two versions of the same Go packages
// and tells whether a Major, Minor, or Patchlevel version bump
// (or None)
// is needed to go from one to the other.
//
// Usage:
//   modver -git REPO [-q] [-v1 OLDERVERSION -v2 NEWERVERSION | -versions] OLDERREV NEWERREV
//   modver [-q] [-v1 OLDERVERSION -v2 NEWERVERSION] OLDERDIR NEWERDIR
//
// With `-git REPO`,
// where REPO is the path to a Git repository,
// OLDER and NEWER are two revisions in the repository
// (e.g. hexadecimal SHA strings or "HEAD", etc)
// containing the older and newer versions of a Go module.
// Without the -git flag,
// OLDER and NEWER are two directories containing the older and newer versions of a Go module.
//
// With -v1 and -v2,
// modver checks whether the change from OLDERVERSION to NEWERVERSION
// (two version strings)
// is adequate for the differences detected between OLDER and NEWER.
// Output is either "OK" or "ERR"
// (followed by a description)
// and the exit code is 0 for OK and 1 for ERR.
// In quiet mode (-q),
// there is no output.
// With -git REPO and -versions instead of -v1 and -v2,
// the values for -v1 and -v2 are determined by querying the repo at the given revisions.
//
// Without -v1 and -v2
// (or -versions),
// output is a string describing the minimum version-number change required.
// In quiet mode (-q),
// there is no output,
// and the exit status is 0, 1, 2, 3, or 4
// for None, Patchlevel, Minor, Major, and error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/bobg/modver"
)

const errorStatus = 4

func main() {
	var (
		gitRepo  = flag.String("git", "", "Git repo URL")
		quiet    = flag.Bool("q", false, "quiet mode: prints no output, exits with status 0, 1, 2, 3, or 4 to mean None, Patchlevel, Minor, Major, or error")
		v1       = flag.String("v1", "", "version string of older version; with -v2 changes output to OK (exit status 0) for adequate version-number change, ERR (exit status 1) for inadequate")
		v2       = flag.String("v2", "", "version string of newer version")
		versions = flag.Bool("versions", false, "with -git, compute values for -v1 and -v2 from the Git repository")
	)
	flag.Parse()

	if *v1 != "" && *v2 != "" {
		if !strings.HasPrefix(*v1, "v") {
			*v1 = "v" + *v1
		}
		if !strings.HasPrefix(*v2, "v") {
			*v2 = "v" + *v2
		}
		if !semver.IsValid(*v1) {
			fmt.Printf("Not a valid version string: %s\n", *v1)
			os.Exit(errorStatus)
		}
		if !semver.IsValid(*v2) {
			fmt.Printf("Not a valid version string: %s\n", *v2)
			os.Exit(errorStatus)
		}
	}

	var (
		res modver.Result
		err error
	)

	if *gitRepo != "" {
		if flag.NArg() != 2 {
			fmt.Printf("Usage: %s -git [-q] [-v1 OLDERVERSION -v2 NEWERVERSION | -versions] OLDERREV NEWERREV\n", os.Args[0])
			os.Exit(errorStatus)
		}

		callback := modver.CompareDirs
		if *versions {
			callback = getTags(v1, v2, flag.Arg(0), flag.Arg(1))
		}
		res, err = modver.CompareGitWith(context.Background(), *gitRepo, flag.Arg(0), flag.Arg(1), callback)
		if err != nil {
			fmt.Printf("Error: %s\n", err)
			os.Exit(errorStatus)
		}
	} else {
		if flag.NArg() != 2 {
			fmt.Printf("Usage: %s [-q] [-v1 OLDERVERSION -v2 NEWERVERSION] OLDERDIR NEWERDIR\n", os.Args[0])
			os.Exit(errorStatus)
		}
		res, err = modver.CompareDirs(flag.Arg(0), flag.Arg(1))
		if err != nil {
			fmt.Printf("Error: %s\n", err)
			os.Exit(errorStatus)
		}
	}

	if *v1 != "" && *v2 != "" {
		var ok bool

		cmp := semver.Compare(*v1, *v2)
		switch res.Code() {
		case modver.None:
			ok = cmp <= 0 // v1 <= v2

		case modver.Patchlevel:
			ok = cmp < 0 // v1 < v2

		case modver.Minor:
			var (
				min1 = semver.MajorMinor(*v1)
				min2 = semver.MajorMinor(*v2)
			)
			ok = semver.Compare(min1, min2) < 0 // min1 < min2

		case modver.Major:
			var (
				maj1 = semver.Major(*v1)
				maj2 = semver.Major(*v2)
			)
			ok = semver.Compare(maj1, maj2) < 0 // maj1 < maj2
		}

		if ok {
			if !*quiet {
				if *versions {
					fmt.Printf("OK using versions %s and %s: %s\n", *v1, *v2, res)
				} else {
					fmt.Printf("OK %s\n", res)
				}
			}
			os.Exit(0)
		}
		if !*quiet {
			if *versions {
				fmt.Printf("OK using versions %s and %s: %s\n", *v1, *v2, res)
			} else {
				fmt.Printf("ERR %s\n", res)
			}
		}
		os.Exit(1)
	}

	if *quiet {
		os.Exit(int(res.Code()))
	}

	fmt.Println(res)
}

func getTags(v1, v2 *string, olderRev, newerRev string) func(older, newer string) (modver.Result, error) {
	return func(older, newer string) (modver.Result, error) {
		tag, err := getTag(older, olderRev)
		if err != nil {
			return modver.None, fmt.Errorf("getting tag from %s: %w", older, err)
		}
		*v1 = tag

		tag, err = getTag(newer, newerRev)
		if err != nil {
			return modver.None, fmt.Errorf("getting tag from %s: %w", newer, err)
		}
		*v2 = tag

		return modver.CompareDirs(older, newer)
	}
}

func getTag(dir, rev string) (string, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", dir, err)
	}
	tags, err := repo.Tags()
	if err != nil {
		return "", fmt.Errorf("getting tags in %s: %w", dir, err)
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		return "", fmt.Errorf(`resolving revision "%s" in %s: %w`, rev, dir, err)
	}
	repoCommit, err := object.GetCommit(repo.Storer, *hash)
	if err != nil {
		return "", fmt.Errorf("getting commit at %s: %w", rev, err)
	}

	var result string

OUTER:
	for {
		tref, err := tags.Next()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return "", fmt.Errorf("iterating over tags in %s: %w", dir, err)
		}
		tag := strings.TrimPrefix(string(tref.Name()), "refs/tags/")
		if !semver.IsValid(tag) {
			continue
		}
		tagCommit, err := object.GetCommit(repo.Storer, tref.Hash())
		if err != nil {
			return "", fmt.Errorf("getting commit for tag %s: %w", tref.Name(), err)
		}
		if tagCommit.Hash != *hash {
			bases, err := repoCommit.MergeBase(tagCommit)
			if err != nil {
				return "", fmt.Errorf("getting merge base of %s and %s: %w", rev, tag, err)
			}
		INNER:
			for _, base := range bases {
				switch base.Hash {
				case *hash:
					// This tag comes later than the checked-out commit.
					continue OUTER
				case tagCommit.Hash:
					// The checked-out commit comes later than the tag.
					break INNER
				}
			}
		}
		if result == "" || semver.Compare(result, tag) < 0 { // result < tag
			result = tag
		}
	}
}
