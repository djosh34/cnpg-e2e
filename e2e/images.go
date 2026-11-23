package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// nodeImagesPinned checks that every image on every node is one a manifest
// of ms names: an image it pins by digest (versions.Manifest.Pinned),
// matched by repository and digest, or one of k3s's airgap images
// (k3s.images), matched by repository and tag and imported by k3s. ms is
// the versions file of the run, after an image upgrade also the one before:
// the nodes keep the images of before. k3s labels each image it imports from
// the airgap tarball with io.cattle.k3s.pinned, and never one containerd
// pulls, so an airgap image that k3s did not import fails too. An error
// names each other image. As an import after a pull of the same tag would
// still carry that label, it also fails when containerd's log shows a pull
// of an airgap image. It runs once the cluster is up, before the scenario,
// and as the last line of always.feature. seen are the tags and digests of
// every image it listed, or its ID for an image with neither.
func nodeImagesPinned(ctx context.Context, ms []*versions.Manifest) (seen []string, _ error) {
	var errs []error
	for i := range nodeCount {
		node := nodeName(i)
		images, err := listImages(ctx, node)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, image := range images {
			seen = append(seen, image.refs()...)
		}
		refs, err := command(ctx, "docker", "exec", node,
			"k3s", "ctr", "-n", "k8s.io", "images", "ls", "-q", `labels."io.cattle.k3s.pinned"==pinned`)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, image := range notInManifest(images, strings.Fields(refs), ms) {
			errs = append(errs, fmt.Errorf("%s has an image the manifest does not name: %s", node, image))
		}
		pulls, err := command(ctx, "docker", "exec", node, "sh", "-c", containerdPulls)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, ref := range pulledAirgap(pulls, ms) {
			errs = append(errs, fmt.Errorf("%s pulled the airgap image %s from a registry", node, ref))
		}
	}
	return seen, errors.Join(errs...)
}

// listImages lists the images on a node with k3s crictl.
func listImages(ctx context.Context, node string) ([]nodeImage, error) {
	out, err := command(ctx, "docker", "exec", node, "k3s", "crictl", "images", "-o", "json")
	if err != nil {
		return nil, err
	}
	images, err := parseImages(out)
	if err != nil {
		return nil, fmt.Errorf("the images of %s: %w", node, err)
	}
	return images, nil
}

// imagesAtEnd are the tags and digests of the images on every node at the
// end of a run, also when the run stopped before it checked them. A node
// that never started has none. For a node it cannot list, it gives
// "unknown: " and why, so a missing list never reads as a clean one.
func imagesAtEnd(ctx context.Context) []string {
	var seen []string
	for i := range nodeCount {
		node := nodeName(i)
		images, err := listImages(ctx, node)
		switch {
		case err != nil && strings.Contains(err.Error(), "No such container"):
		case err != nil:
			seen = append(seen, "unknown: "+strings.ReplaceAll(err.Error(), "\n", " "))
		}
		for _, image := range images {
			seen = append(seen, image.refs()...)
		}
	}
	return seen
}

// containerdPulls prints the lines of containerd's log on a node about
// pulls: containerd writes "stop pulling image <ref>:" for every pull it
// makes, also of the sandbox image. k3s rotates the log at 50 MiB into
// gzipped files; the script fails then, as the log no longer holds every
// pull. It looks for rotated files after grep has read the log, so a
// rotation while grep opens it fails too, and a match of the pattern is
// enough, as gzip may rename the file right after. grep exits 1 when no line
// matches: no image was pulled.
const containerdPulls = `cd /var/lib/rancher/k3s/agent/containerd || exit
grep -h 'stop pulling image ' containerd.log || [ $? = 1 ] || exit
for f in containerd-*; do
  if [ "$f" != 'containerd-*' ]; then echo "containerd's log was rotated ($f), so it no longer shows every pull" >&2; exit 3; fi
done`

var pullLine = regexp.MustCompile(`stop pulling image (\S+): `)

// pulledAirgap are the airgap images of the manifests that a log of
// containerd shows a pull of.
func pulledAirgap(log string, ms []*versions.Manifest) []string {
	airgap := airgapImages(ms)
	var pulled []string
	for _, line := range pullLine.FindAllStringSubmatch(log, -1) {
		ref := versions.Reference(line[1])
		if slices.Contains(airgap, ref) && !slices.Contains(pulled, ref) {
			pulled = append(pulled, ref)
		}
	}
	return pulled
}

// airgapImages are the airgap images of the manifests, each in its full
// form.
func airgapImages(ms []*versions.Manifest) []string {
	var refs []string
	for _, m := range ms {
		for _, ref := range m.K3s.Images {
			refs = append(refs, versions.Reference(ref))
		}
	}
	return refs
}

// pinnedImages are the images the manifests pin by digest, as
// repository@digest.
func pinnedImages(ms []*versions.Manifest) []string {
	var pinned []string
	for _, m := range ms {
		pinned = append(pinned, m.Pinned()...)
	}
	return pinned
}

// nodeImage is an image of crictl images -o json.
type nodeImage struct {
	ID          string   `json:"id"`
	RepoTags    []string `json:"repoTags"`
	RepoDigests []string `json:"repoDigests"`
}

// refs are an image's tags and digests, or its ID when it has neither.
func (i nodeImage) refs() []string {
	refs := append(append([]string{}, i.RepoTags...), i.RepoDigests...)
	if len(refs) == 0 {
		return []string{i.ID}
	}
	return refs
}

func parseImages(out string) ([]nodeImage, error) {
	var list struct {
		Images []nodeImage `json:"images"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, err
	}
	return list.Images, nil
}

// notInManifest names each image that is neither pinned by a manifest of
// ms nor one of their airgap images that k3s imported, by its tags and
// digests. imported are the references of the images k3s imported.
func notInManifest(images []nodeImage, imported []string, ms []*versions.Manifest) []string {
	pinned := pinnedImages(ms)
	airgap := map[string]bool{}
	for _, ref := range airgapImages(ms) {
		if slices.ContainsFunc(imported, func(i string) bool { return versions.Reference(i) == ref }) {
			airgap[ref] = true
		}
	}
	var other []string
	for _, image := range images {
		known := slices.ContainsFunc(image.RepoTags, func(tag string) bool { return airgap[versions.Reference(tag)] }) ||
			slices.ContainsFunc(image.RepoDigests, func(d string) bool {
				name, digest, _ := strings.Cut(d, "@")
				return slices.Contains(pinned, versions.Repository(name)+"@"+digest)
			})
		if !known {
			other = append(other, strings.Join(image.refs(), " ")+" ("+image.ID+")")
		}
	}
	return other
}

// imagesPinned: every image on the nodes is one the versions file names, or
// after an image upgrade the one before.
func imagesPinned(ctx context.Context, s *scenario, _ args) error {
	seen, err := nodeImagesPinned(ctx, s.w.manifests())
	s.images = append(s.images, seen...)
	return err
}
