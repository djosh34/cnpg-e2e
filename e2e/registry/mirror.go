package registry

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"github.com/regclient/regclient"
	"github.com/regclient/regclient/types/manifest"
	"github.com/regclient/regclient/types/platform"
	"github.com/regclient/regclient/types/ref"
)

// Every package is a copy of the Docker Hardened Image or chart of the same
// name on dhi.io, except these, by package name.
var sources = map[string]string{
	// DHI has no barman-cloud chart.
	"plugin-barman-cloud": "ghcr.io/cloudnative-pg/charts/plugin-barman-cloud",
}

// Mirror copies each image and chart of the versions file at file into
// this repo's packages, from its source, with its signatures and
// attestations, and writes the tag and digest it copied into the file. It
// copies by the tag the file names, or with latest by the newest tag of the
// same kind at the source (see newestTag). Each package must be public
// before a run without a login can pull it.
func Mirror(ctx context.Context, file string, latest bool) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	data, err = versions.Update(data, func(p versions.Pin) (versions.Pin, error) {
		name, ok := strings.CutPrefix(p.Repository, versions.Packages+"/")
		if !ok || p.Tag == "" {
			return p, fmt.Errorf("%s is not one of this repo's packages by tag", p)
		}
		src := "dhi.io/" + name
		if s, ok := sources[name]; ok {
			src = s
		}
		// A new client for each package: with one client for all of them,
		// dhi.io answered unauthorized from the second repository on.
		rc := Client()
		tag := p.Tag
		if latest {
			tags, err := tagList(ctx, rc, src)
			if err != nil {
				return p, err
			}
			if tag, err = newestTag(tags, p.Tag); err != nil {
				return p, fmt.Errorf("%s: %w", src, err)
			}
		}
		digest, err := mirror(ctx, rc, src+":"+tag, p.Repository+":"+tag)
		if err != nil {
			return p, err
		}
		log.Printf("mirror: %s:%s@%s", p.Repository, tag, digest)
		return versions.Pin{Repository: p.Repository, Tag: tag, Digest: digest}, nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return os.WriteFile(file, data, 0o644)
}

// tagList is every tag of the repository src.
func tagList(ctx context.Context, rc *regclient.RegClient, src string) ([]string, error) {
	r, err := ref.New(src)
	if err != nil {
		return nil, err
	}
	tl, err := rc.TagList(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	return tl.GetTags()
}

// versionTag is a tag that starts with a version: an optional v, numbers
// with dots, then the rest, such as -debian13.
var versionTag = regexp.MustCompile(`^(v?)([0-9]+(?:\.[0-9]+)*)(.*)$`)

// newestTag is the newest of tags of the same kind as current: the same
// first number, as many numbers and the same text around them. For
// 18.7-debian13 it may be 18.8-debian13, never 19.1-debian13, 18.8 or
// 18.8-alpine3.24. A new first number can need more than a new image, such
// as a PostgreSQL major upgrade, so it stays a change by hand. It is
// current when no tag is newer.
func newestTag(tags []string, current string) (string, error) {
	want := versionTag.FindStringSubmatch(current)
	if want == nil {
		return "", fmt.Errorf("the tag %s does not start with a version", current)
	}
	wantNums, err := numbers(want[2])
	if err != nil {
		return "", err
	}
	best, bestNums := current, wantNums
	for _, t := range tags {
		got := versionTag.FindStringSubmatch(t)
		if got == nil || got[1] != want[1] || got[3] != want[3] {
			continue
		}
		nums, err := numbers(got[2])
		if err != nil || len(nums) != len(wantNums) || nums[0] != wantNums[0] {
			continue
		}
		if slices.Compare(nums, bestNums) > 0 {
			best, bestNums = t, nums
		}
	}
	return best, nil
}

// numbers are the numbers of a version such as 1.21.3.
func numbers(version string) ([]int, error) {
	var nums []int
	for _, f := range strings.Split(version, ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, err
		}
		nums = append(nums, n)
	}
	return nums, nil
}

// mirror copies the image or chart src, by tag, to dst, by the same tag:
// an image's whole index, with its signatures and attestations. It returns
// the digest of the linux/amd64 image, which the nodes run, or of the
// chart.
func mirror(ctx context.Context, rc *regclient.RegClient, src, dst string) (string, error) {
	r, err := ref.New(src)
	if err != nil {
		return "", err
	}
	m, err := rc.ManifestGet(ctx, r)
	if err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	digest := m.GetDescriptor().Digest.String()
	// By digest, so the copy is the one the file gets, even when the tag
	// moves meanwhile.
	err = copyImage(ctx, rc, src+"@"+digest, dst, regclient.ImageWithReferrers(), regclient.ImageWithDigestTags())
	if err != nil || !m.IsList() {
		return digest, err
	}
	amd64, err := manifest.GetPlatformDesc(m, &platform.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	return amd64.Digest.String(), nil
}
