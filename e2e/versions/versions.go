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
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var m Manifest
	if err := doc.Decode(&m); err != nil {
		return nil, err
	}
	var all any
	if err := doc.Decode(&all); err != nil {
		return nil, err
	}
	var w images
	w.visit(&doc, "")
	pinned := make([]Pin, 0, len(w.sites))
	for _, s := range w.sites {
		pinned = append(pinned, s.pin)
	}
	slices.SortFunc(pinned, func(a, b Pin) int { return strings.Compare(a.String(), b.String()) })
	m.images = slices.Compact(pinned)
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

// images walks the manifest for its images: where it pins each, the paths
// of the image blocks without a digest, and of those with a digest both in
// digest and in tag, of which a chart reads only one. charts holds chart
// repositories, not images, and k3s its airgap images by tag, which k3s
// imports.
type images struct {
	sites                []site
	unpinned, twoDigests []string
}

// site is where the manifest pins an image or a chart: the scalars of its
// tag and of its digest. When one string holds both, both are that scalar,
// and prefix is what comes before the tag in it, such as the repository and
// its colon.
type site struct {
	pin         Pin
	tag, digest *yaml.Node
	prefix      string
}

func (w *images) visit(n *yaml.Node, path string) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			w.visit(c, path)
		}
	case yaml.ScalarNode:
		if name, digest, ok := strings.Cut(n.Value, "@"); ok && strings.HasPrefix(digest, "sha256:") {
			t := tag(name)
			w.sites = append(w.sites, site{
				pin: Pin{Repository: Repository(name), Tag: t, Digest: digest},
				tag: n, digest: n, prefix: strings.TrimSuffix(name, t),
			})
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			w.visit(c, fmt.Sprintf("%s[%d]", path, i))
		}
	case yaml.MappingNode:
		fields := fields(n)
		if repo := scalar(fields["repository"]); repo != "" {
			if registry := scalar(fields["registry"]); registry != "" {
				repo = registry + "/" + repo
			}
			digestNode, tagNode := fields["digest"], fields["tag"]
			digest := scalar(digestNode)
			tag, inTag, _ := strings.Cut(scalar(tagNode), "@")
			switch {
			case digest != "" && inTag != "":
				w.twoDigests = append(w.twoDigests, path)
			case digest == "":
				digest, digestNode = inTag, tagNode
			}
			if strings.HasPrefix(digest, "sha256:") {
				w.sites = append(w.sites, site{pin: Pin{Repository: Repository(repo), Tag: tag, Digest: digest}, tag: tagNode, digest: digestNode})
			} else {
				w.unpinned = append(w.unpinned, path)
			}
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			// A tag with a digest belongs to the block above.
			if k == "tag" || (path == "" && (k == "charts" || k == "k3s")) {
				continue
			}
			w.visit(n.Content[i+1], strings.TrimPrefix(path+"."+k, "."))
		}
	}
}

// scalar is the value of a scalar node, or "" for anything else.
func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// fields are the values of a mapping node by their keys.
func fields(n *yaml.Node) map[string]*yaml.Node {
	f := map[string]*yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		f[n.Content[i].Value] = n.Content[i+1]
	}
	return f
}

// chartSites are where the manifest, a document Parse took, pins its
// charts: the version and digest of each, as a Pin of its OCI repository
// and name.
func chartSites(doc *yaml.Node) ([]site, error) {
	var sites []site
	charts := fields(doc.Content[0])["charts"]
	if charts == nil {
		return nil, errors.New("charts is not a key of the file itself, such as one of a YAML merge")
	}
	for i := 1; i < len(charts.Content); i += 2 {
		f := fields(charts.Content[i])
		repo := strings.TrimPrefix(scalar(f["repository"]), "oci://") + "/" + scalar(f["name"])
		sites = append(sites, site{
			pin: Pin{Repository: repo, Tag: scalar(f["version"]), Digest: scalar(f["digest"])},
			tag: f["version"], digest: f["digest"],
		})
	}
	return sites, nil
}

// Update is the versions file data with each image and chart it pins moved
// to the tag and digest that move gives for it, with the file's comments
// and layout kept. It calls move once for each pin.
func Update(data []byte, move func(Pin) (Pin, error)) ([]byte, error) {
	if _, err := Parse(data); err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var w images
	w.visit(&doc, "")
	charts, err := chartSites(&doc)
	if err != nil {
		return nil, err
	}
	moved := map[Pin]Pin{}
	var edits []edit
	for _, s := range append(w.sites, charts...) {
		to, ok := moved[s.pin]
		if !ok {
			var err error
			if to, err = move(s.pin); err != nil {
				return nil, err
			}
			moved[s.pin] = to
		}
		switch {
		case to == s.pin:
		case s.tag == s.digest:
			edits = append(edits, edit{s.tag, s.prefix + to.Tag + "@" + to.Digest})
		case to.Tag != s.pin.Tag && s.tag == nil:
			return nil, fmt.Errorf("%s has no tag to move to %s", s.pin, to.Tag)
		default:
			edits = append(edits, edit{s.digest, to.Digest})
			if to.Tag != s.pin.Tag {
				edits = append(edits, edit{s.tag, to.Tag})
			}
		}
	}
	out, err := apply(data, edits)
	if err != nil {
		return nil, err
	}
	if _, err := Parse(out); err != nil {
		return nil, fmt.Errorf("the new versions file: %w", err)
	}
	return out, nil
}

// edit sets the scalar at its node's place in the file to value.
type edit struct {
	at    *yaml.Node
	value string
}

// apply makes the edits in data, each in place of its scalar's text, in its
// quotes.
func apply(data []byte, edits []edit) ([]byte, error) {
	// From the end, so an edit leaves the places of the others as they are.
	slices.SortFunc(edits, func(a, b edit) int {
		if a.at.Line != b.at.Line {
			return b.at.Line - a.at.Line
		}
		return b.at.Column - a.at.Column
	})
	lines := strings.SplitAfter(string(data), "\n")
	for _, e := range edits {
		quote := ""
		switch e.at.Style {
		case 0:
		case yaml.DoubleQuotedStyle:
			quote = `"`
		case yaml.SingleQuotedStyle:
			quote = "'"
		default:
			return nil, fmt.Errorf("line %d: the scalar %q is not plain or quoted", e.at.Line, e.at.Value)
		}
		line, col := e.at.Line-1, e.at.Column-1
		old := quote + e.at.Value + quote
		if line >= len(lines) || col > len(lines[line]) || !strings.HasPrefix(lines[line][col:], old) {
			return nil, fmt.Errorf("line %d: no %s at column %d", e.at.Line, old, e.at.Column)
		}
		lines[line] = lines[line][:col] + quote + e.value + quote + lines[line][col+len(old):]
	}
	return []byte(strings.Join(lines, "")), nil
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
