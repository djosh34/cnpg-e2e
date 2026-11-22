// Package images holds the images this repo builds for the e2e test, one
// per directory here: the nodes, the controller and the services outside
// k3s that need setup. Each is a package of this repo, tagged by a hash of
// its directory, so a change of any file gives a new tag. Get pulls an
// image, or builds it where its package does not have that tag yet, as on
// a branch that changed it; Publish builds and pushes each missing tag.
package images

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"github.com/regclient/regclient"
	"github.com/regclient/regclient/types/ref"
)

//go:embed acme alertmanager controller dns grafana node otel prometheus
var files embed.FS

// Names are the names of the images, their directories.
func Names() ([]string, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// Read is the content of a file of an image, by its path here, such as
// prometheus/rules.yaml.
func Read(path string) (string, error) {
	data, err := files.ReadFile(path)
	return string(data), err
}

// Ref is the image's reference: its package by the hash of its directory.
func Ref(name string) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(files, name, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(h, "%s %d\n", path, len(data)); err != nil {
			return err
		}
		_, err = h.Write(data)
		return err
	})
	if err != nil {
		return "", err
	}
	return versions.Packages + "/" + name + ":" + hex.EncodeToString(h.Sum(nil))[:20], nil
}

// Get returns the image's reference once Docker has the image: already
// there, pulled, or built here when its package does not have it.
func Get(ctx context.Context, name string) (string, error) {
	image, err := Ref(name)
	if err != nil {
		return "", err
	}
	if docker(ctx, "image", "inspect", image) == nil {
		return image, nil
	}
	pullErr := docker(ctx, "pull", "--quiet", image)
	if pullErr == nil {
		return image, nil
	}
	log.Printf("images: building %s, since pulling it failed: %v", image, pullErr)
	return image, build(ctx, name, image)
}

// Publish builds and pushes each image whose package does not have its
// tag yet. It needs docker login to ghcr.io with the right to push this
// repo's packages: only this repo's workflows push them.
func Publish(ctx context.Context, rc *regclient.RegClient) error {
	names, err := Names()
	if err != nil {
		return err
	}
	for _, name := range names {
		image, err := Ref(name)
		if err != nil {
			return err
		}
		r, err := ref.New(image)
		if err != nil {
			return err
		}
		if _, err := rc.ManifestHead(ctx, r); err == nil {
			log.Printf("images: %s is there", image)
			continue
		}
		if err := build(ctx, name, image); err != nil {
			return err
		}
		if err := docker(ctx, "push", "--quiet", image); err != nil {
			return err
		}
		log.Printf("images: pushed %s", image)
	}
	return nil
}

// build builds the image from its directory, sent to docker build as a tar
// archive, and tags it image.
func build(ctx context.Context, name, image string) error {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	err := fs.WalkDir(files, name, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		header := &tar.Header{Name: strings.TrimPrefix(path, name+"/"), Mode: 0o644, Size: int64(len(data))}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err := errors.Join(err, tw.Close()); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "docker", "build", "--tag", image, "-")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = &archive, os.Stdout, os.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build of %s: %w", image, err)
	}
	return nil
}

// docker runs the docker command. A failure's error holds its output.
func docker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out.Bytes()))
	}
	return nil
}
