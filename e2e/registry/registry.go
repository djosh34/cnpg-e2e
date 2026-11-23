// Package registry copies images and charts from registry to registry,
// keeping their digests. Fill copies the images of the versions file into
// the registry the nodes pull from; Mirror copies the run's images and
// charts from their sources into this repo's packages.
package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"github.com/regclient/regclient"
	"github.com/regclient/regclient/config"
	"github.com/regclient/regclient/types/ref"
)

// Client is a registry client with the logins of docker login. Each of
// plainHTTP is a host:port that it reaches over HTTP instead of HTTPS.
func Client(plainHTTP ...string) *regclient.RegClient {
	opts := []regclient.Opt{regclient.WithDockerCreds(), regclient.WithUserAgent("cnpg-e2e")}
	for _, host := range plainHTTP {
		opts = append(opts, regclient.WithConfigHost(config.Host{Name: host, TLS: config.TLSDisabled}))
	}
	return regclient.New(opts...)
}

// Fill copies every image the versions file pins into the registry at host,
// under the same repository path without the source's host, so that a node
// with that registry as the mirror of the source's host finds each by its
// digest. The versions file pins the linux/amd64 image, not the index, so
// only that image is copied.
func Fill(ctx context.Context, rc *regclient.RegClient, m *versions.Manifest, host string) error {
	for _, image := range m.Pinned() {
		repo, digest, _ := strings.Cut(image, "@")
		_, path, _ := strings.Cut(repo, "/")
		if err := copyImage(ctx, rc, image, host+"/"+path+"@"+digest); err != nil {
			return err
		}
	}
	return nil
}

// copyImage copies the image or chart src to dst.
func copyImage(ctx context.Context, rc *regclient.RegClient, src, dst string, opts ...regclient.ImageOpts) error {
	from, err := ref.New(src)
	if err != nil {
		return err
	}
	to, err := ref.New(dst)
	if err != nil {
		return err
	}
	if err := rc.ImageCopy(ctx, from, to, opts...); err != nil {
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	return nil
}
