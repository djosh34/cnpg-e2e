// Package registry copies images and charts between registries, keeping
// their digests.
package registry

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/regclient/regclient"
	"github.com/regclient/regclient/config"
	"github.com/regclient/regclient/types/ref"
)

// Client is a registry client with the logins of docker login. It reaches
// each host of plainHTTP over HTTP.
func Client(plainHTTP ...string) *regclient.RegClient {
	opts := []regclient.Opt{regclient.WithDockerCreds(), regclient.WithUserAgent("cnpg-e2e")}
	for _, host := range plainHTTP {
		opts = append(opts, regclient.WithConfigHost(config.Host{Name: host, TLS: config.TLSDisabled}))
	}
	return regclient.New(opts...)
}

// Copy copies each image, as repository@digest, into the registry at host
// under the same path without the source's host, where a node that mirrors
// the source's host through that registry finds it by its digest. The
// digests are of linux/amd64 images, not of indexes, so only that image is
// copied.
func Copy(ctx context.Context, rc *regclient.RegClient, images []string, host string) error {
	for _, image := range images {
		repo, digest, _ := strings.Cut(image, "@")
		_, path, _ := strings.Cut(repo, "/")
		if err := copyImage(ctx, rc, image, host+"/"+path+"@"+digest); err != nil {
			return err
		}
	}
	return nil
}

// Export writes the image as an archive that k3s imports from its images
// directory.
func Export(ctx context.Context, rc *regclient.RegClient, image string, w io.Writer) error {
	r, err := ref.New(image)
	if err != nil {
		return err
	}
	if err := rc.ImageExport(ctx, r, w); err != nil {
		return fmt.Errorf("exporting %s: %w", image, err)
	}
	return nil
}

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
