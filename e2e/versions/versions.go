// Package versions reads the versions file, versions/versions.yaml: every
// version of the system under test that an e2e run uses.
// helm/helmfile.yaml.gotmpl reads the same file and gives each release its
// images from it.
package versions

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Packages is where this repo publishes its images and charts. The
// versions file names only images and charts there, besides k3s's own.
const Packages = "ghcr.io/djosh34/cnpg-e2e"

// Manifest is what the e2e run reads from a manifest besides the charts'
// own values, which only helmfile reads.
type Manifest struct {
	K3s        K3s              `yaml:"k3s"`
	Tools      Tools            `yaml:"tools"`
	Charts     map[string]Chart `yaml:"charts"`
	Registry   Registry         `yaml:"registry"`
	PostgreSQL PostgreSQL       `yaml:"postgresql"`

	// images are the images the manifest pins by digest, each once.
	images []Pin
}

// Registry is the registry of the nodes: its image by tag and digest.
type Registry struct {
	Image string `yaml:"image"`
}

// Pin is an image the manifest pins: its repository in its full form, its
// tag and its digest.
type Pin struct {
	Repository, Tag, Digest string
}

// K3s is the k3s release of the nodes.
type K3s struct {
	Version string `yaml:"version"`
	// The airgap image tarball of each architecture, which k3s imports at
	// start.
	Airgap map[string]Download `yaml:"airgap"`
	// The images in the tarballs, the release's k3s-images.txt.
	Images []string `yaml:"images"`
	// The release one minor before, which the k3s upgrade scenarios start
	// from.
	Previous struct {
		Version string `yaml:"version"`
	} `yaml:"previous"`
}

// Download is a file of a release and its sha256.
type Download struct {
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
}

// Tools are the tools of the e2e test that have to match the cluster.
type Tools struct {
	Kubectl     Tool `yaml:"kubectl"`
	KubectlCNPG Tool `yaml:"kubectl-cnpg"`
}

// Tool is a binary with the sha256 of its download for each architecture.
type Tool struct {
	Version string            `yaml:"version"`
	SHA256  map[string]string `yaml:"sha256"`
}

// Chart is an upstream chart in an OCI registry: Name is the chart's own
// name, such as cert-manager-chart for the key cert-manager, and Digest the
// digest of its OCI manifest.
type Chart struct {
	Name       string `yaml:"name"`
	Repository string `yaml:"repository"`
	Version    string `yaml:"version"`
	Digest     string `yaml:"digest"`
}

// PostgreSQL is the instance image, which the cnpg-cluster chart reads.
// PostgresUID and PostgresGID are the ids it names postgres, which the
// cluster gets.
type PostgreSQL struct {
	ImageName   string `yaml:"imageName"`
	PostgresUID int    `yaml:"postgresUID"`
	PostgresGID int    `yaml:"postgresGID"`
}

var (
	sha256Hex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// An exact chart version, not a range: Helm would pick a newer chart.
	exactVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
)

// Load reads a manifest and checks that every part the run reads is there.
func Load(file string) (*Manifest, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	m, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return m, nil
}

// Parse reads a manifest from its content.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	var all any
	if err := yaml.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	var w images
	w.visit(all, "")
	slices.SortFunc(w.pinned, func(a, b Pin) int { return strings.Compare(a.String(), b.String()) })
	m.images = slices.Compact(w.pinned)
	slices.Sort(w.unpinned)
	errs := []error{m.check()}
	for _, path := range w.unpinned {
		errs = append(errs, fmt.Errorf("the image %s has no digest", path))
	}
	for _, path := range w.twoDigests {
		errs = append(errs, fmt.Errorf("the image %s has a digest in digest and in tag", path))
	}
	for _, c := range chartImages {
		if !c.pinnedIn(all) {
			errs = append(errs, fmt.Errorf("no image %s with a repository and its digest in %s", strings.Join(c.keys, "."), c.field))
		}
	}
	return &m, errors.Join(errs...)
}

func (m *Manifest) check() error {
	var errs []error
	need := func(ok bool, what string) {
		if !ok {
			errs = append(errs, fmt.Errorf("no %s", what))
		}
	}
	need(m.K3s.Version != "", "k3s.version")
	need(len(m.K3s.Images) > 0, "k3s.images")
	// The run is amd64 only.
	a := m.K3s.Airgap["amd64"]
	need(a.URL != "" && sha256Hex.MatchString(a.SHA256), "k3s.airgap.amd64 with url and sha256")
	need(sha256Hex.MatchString(m.Tools.Kubectl.SHA256["amd64"]), "tools.kubectl.sha256.amd64")
	need(sha256Hex.MatchString(m.Tools.KubectlCNPG.SHA256["amd64"]), "tools.kubectl-cnpg.sha256.amd64")
	need(m.Tools.Kubectl.Version != "", "tools.kubectl.version")
	need(m.Tools.KubectlCNPG.Version != "", "tools.kubectl-cnpg.version")
	need(len(m.Charts) > 0, "charts")
	for name, c := range m.Charts {
		need(c.Repository != "" && exactVersion.MatchString(c.Version), "charts."+name+" with repository and an exact version")
		need(sha256Digest.MatchString(c.Digest), "charts."+name+".digest")
		need(c.Name != "", "charts."+name+".name")
	}
	need(strings.Contains(m.Registry.Image, "@sha256:"), "registry.image with a digest")
	need(strings.Contains(m.PostgreSQL.ImageName, "@sha256:"), "postgresql.imageName with a digest")
	need(m.PostgreSQL.PostgresUID > 0 && m.PostgreSQL.PostgresGID > 0, "postgresql.postgresUID and postgresGID, both above 0")
	return errors.Join(errs...)
}

// Pinned are the images the manifest pins by digest, each as
// repository@digest with the repository in its full form, such as
// docker.io/library/busybox. They are the images of every string such as
// registry.example/name:tag@sha256:…, and of every image block of a chart's
// values: a map with repository (after registry when there is one) and
// either digest or a tag that ends in @sha256:…. k3s's own images are not
// among them: k3s imports them from its airgap tarball.
func (m *Manifest) Pinned() []string {
	pinned := make([]string, 0, len(m.images))
	for _, p := range m.images {
		pinned = append(pinned, p.Repository+"@"+p.Digest)
	}
	slices.Sort(pinned)
	return slices.Compact(pinned)
}

// Images are the images of Pinned with their tags, each once.
func (m *Manifest) Images() []Pin { return m.images }

func (p Pin) String() string { return p.Repository + ":" + p.Tag + "@" + p.Digest }

// chartImage is an image block of an upstream chart: its keys in the
// manifest, and the field the chart reads its digest from: digest, or tag
// for a chart without a digest field.
type chartImage struct {
	keys  []string
	field string
}

// chartImages are the image blocks the upstream charts read. Each must be
// there with its digest where its chart reads it: a chart without one runs
// its own default image, by tag only.
var chartImages = []chartImage{
	{[]string{"cert-manager", "image"}, "digest"},
	{[]string{"cert-manager", "webhook", "image"}, "digest"},
	{[]string{"cert-manager", "cainjector", "image"}, "digest"},
	{[]string{"cert-manager", "acmesolver", "image"}, "digest"},
	{[]string{"cert-manager", "startupapicheck", "image"}, "digest"},
	{[]string{"cloudnative-pg", "image"}, "tag"},
	{[]string{"plugin-barman-cloud", "image"}, "tag"},
	{[]string{"plugin-barman-cloud", "sidecarImage"}, "tag"},
	{[]string{"agent", "image"}, "digest"},
	{[]string{"cluster", "image"}, "digest"},
}

// pinnedIn says whether the manifest has the block, by its nested keys,
// with a repository and a digest in its field.
func (c chartImage) pinnedIn(manifest any) bool {
	node := manifest
	for _, k := range c.keys {
		m, ok := node.(map[string]any)
		if !ok {
			return false
		}
		node = m[k]
	}
	block, ok := node.(map[string]any)
	if !ok {
		return false
	}
	if repo, ok := block["repository"].(string); !ok || repo == "" {
		return false
	}
	value, ok := block[c.field].(string)
	if !ok {
		return false
	}
	if c.field == "tag" {
		_, value, _ = strings.Cut(value, "@")
	}
	return strings.HasPrefix(value, "sha256:")
}

// images walks the manifest for its images: those it pins, the paths of
// the image blocks without a digest, and of those with a digest both in
// digest and in tag, of which a chart reads only one. charts holds chart
// repositories, not images, and k3s its airgap images by tag, which k3s
// imports.
type images struct {
	pinned               []Pin
	unpinned, twoDigests []string
}

func (w *images) visit(node any, path string) {
	switch n := node.(type) {
	case string:
		if name, digest, ok := strings.Cut(n, "@"); ok && strings.HasPrefix(digest, "sha256:") {
			w.pinned = append(w.pinned, Pin{Repository: Repository(name), Tag: tag(name), Digest: digest})
		}
	case []any:
		for i, v := range n {
			w.visit(v, fmt.Sprintf("%s[%d]", path, i))
		}
	case map[string]any:
		if repo, ok := n["repository"].(string); ok && repo != "" {
			if registry, ok := n["registry"].(string); ok && registry != "" {
				repo = registry + "/" + repo
			}
			var digest, tag, inTag string
			if d, ok := n["digest"].(string); ok {
				digest = d
			}
			if t, ok := n["tag"].(string); ok {
				tag, inTag, _ = strings.Cut(t, "@")
			}
			switch {
			case digest != "" && inTag != "":
				w.twoDigests = append(w.twoDigests, path)
			case digest == "":
				digest = inTag
			}
			if strings.HasPrefix(digest, "sha256:") {
				w.pinned = append(w.pinned, Pin{Repository: Repository(repo), Tag: tag, Digest: digest})
			} else {
				w.unpinned = append(w.unpinned, path)
			}
		}
		for k, v := range n {
			// A tag with a digest belongs to the block above.
			if k == "tag" || (path == "" && (k == "charts" || k == "k3s")) {
				continue
			}
			w.visit(v, strings.TrimPrefix(path+"."+k, "."))
		}
	}
}

// Repository is the full form of an image name, with its tag cut off:
// docker.io and library/ where the name leaves them out, as containerd
// writes them.
func Repository(name string) string {
	// A tag is after the last colon, unless that colon is in the registry's
	// host:port.
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	first, rest, ok := strings.Cut(name, "/")
	if !ok {
		return "docker.io/library/" + name
	}
	if !strings.ContainsAny(first, ".:") && first != "localhost" {
		return "docker.io/" + name
	}
	if first == "docker.io" && !strings.Contains(rest, "/") {
		return "docker.io/library/" + rest
	}
	return name
}

// tag is the tag of an image name, or "" when it has none.
func tag(name string) string {
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		return name[i+1:]
	}
	return ""
}

// Reference is the full form of an image reference by tag, such as
// docker.io/rancher/mirrored-pause:3.10.2.
func Reference(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return Repository(ref) + ref[i:]
	}
	return Repository(ref)
}
