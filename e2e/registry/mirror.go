package registry

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"github.com/regclient/regclient"
	"github.com/regclient/regclient/types/ref"
)

// Every package is a copy of the Docker Hardened Image or chart of the same
// name on dhi.io, except these, by package name.
var sources = map[string]string{
	// DHI has no barman-cloud chart.
	"plugin-barman-cloud": "ghcr.io/cloudnative-pg/charts/plugin-barman-cloud",
}

// Mirror copies each image and chart of the versions file at file into
// this repo's packages, from its source by the tag the file names, with its
// signatures and attestations. It then writes the digest each tag had at
// its source into the file, in place of the one there. Each package must be
// public before a run without a login can pull it.
func Mirror(ctx context.Context, rc *regclient.RegClient, file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	m, err := versions.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	pins := m.Images()
	for key, c := range m.Charts {
		name := c.Name
		if name == "" {
			name = key
		}
		pins = append(pins, versions.Pin{Repository: strings.TrimPrefix(c.Repository, "oci://") + "/" + name, Tag: c.Version, Digest: c.Digest})
	}
	// The new digest of each digest in the file. Two images with the same
	// digest, such as the two collectors', are the same tag of one package.
	copied := map[string]string{}
	for _, p := range pins {
		name, ok := strings.CutPrefix(p.Repository, versions.Packages+"/")
		if !ok || p.Tag == "" {
			return fmt.Errorf("%s is not one of this repo's packages by tag", p)
		}
		src := "dhi.io/" + name
		if s, ok := sources[name]; ok {
			src = s
		}
		digest, err := mirror(ctx, rc, src+":"+p.Tag, p.Repository+":"+p.Tag)
		if err != nil {
			return err
		}
		if d, ok := copied[p.Digest]; ok && d != digest {
			return fmt.Errorf("%s and another image share the digest %s in the file, but not at their sources", p, p.Digest)
		}
		copied[p.Digest] = digest
		log.Printf("mirror: %s:%s@%s", p.Repository, p.Tag, digest)
	}
	pairs := make([]string, 0, 2*len(copied))
	for old, digest := range copied {
		pairs = append(pairs, old, digest)
	}
	// One pass, so a new digest is never replaced again.
	data = []byte(strings.NewReplacer(pairs...).Replace(string(data)))
	if _, err := versions.Parse(data); err != nil {
		return fmt.Errorf("the new versions file: %w", err)
	}
	return os.WriteFile(file, data, 0o644)
}

// mirror copies the image or chart src, by tag, to dst, by the same tag, and
// returns its digest.
func mirror(ctx context.Context, rc *regclient.RegClient, src, dst string) (string, error) {
	r, err := ref.New(src)
	if err != nil {
		return "", err
	}
	m, err := rc.ManifestHead(ctx, r, regclient.WithManifestRequireDigest())
	if err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	digest := m.GetDescriptor().Digest.String()
	// By digest, so the copy is the one the file gets, even when the tag
	// moves meanwhile.
	err = copyImage(ctx, rc, src+"@"+digest, dst, regclient.ImageWithReferrers(), regclient.ImageWithDigestTags())
	return digest, err
}
