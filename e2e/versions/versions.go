// Package versions reads versions/versions.yaml. helm/helmfile.yaml.gotmpl
// reads the same file and gives each release its images from it.
package versions

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// File is the versions file, from the root of the checkout.
const File = "versions/versions.yaml"

// Packages is where this repo publishes its images and charts.
const Packages = "ghcr.io/djosh34/cnpg-e2e"

type Manifest struct {
	K3s       K3s                 `yaml:"k3s"`
	SELinux   map[string]Download `yaml:"selinux"`
	NodeTools map[string]Download `yaml:"nodeTools"`
	Charts    map[string]Chart    `yaml:"charts"`
	Registry  struct {
		Image string `yaml:"image"`
	} `yaml:"registry"`
	PostgreSQL struct {
		ImageName string `yaml:"imageName"`
	} `yaml:"postgresql"`
	Lab Lab `yaml:"lab"`

	// Every image of the cluster as repository@digest, each once.
	Pinned []string `yaml:"-"`
}

type K3s struct {
	Version  string   `yaml:"version"`
	Binary   Download `yaml:"binary"`
	Install  Download `yaml:"install"`
	Airgap   Download `yaml:"airgap"`
	RotateCA Download `yaml:"rotate-ca"`
	// The images of the airgap tarball, which k3s imports by tag.
	Images []string `yaml:"images"`
}

type Download struct {
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
}

// Chart is an upstream chart: name is its name in the OCI repository.
type Chart struct {
	Name       string `yaml:"name"`
	Repository string `yaml:"repository"`
	Version    string `yaml:"version"`
}

// Lab is what the e2e lab runs: the images and the tools, besides the
// cluster.
type Lab struct {
	Node         string   `yaml:"node"`
	Controller   string   `yaml:"controller"`
	AnsibleCore  string   `yaml:"ansible-core"`
	VM           string   `yaml:"vm"`
	VMDisk       Download `yaml:"vm-disk"`
	DNS          string   `yaml:"dns"`
	ACME         string   `yaml:"acme"`
	S3           string   `yaml:"s3"`
	OTel         string   `yaml:"otel"`
	Prometheus   string   `yaml:"prometheus"`
	Alertmanager string   `yaml:"alertmanager"`
	Grafana      string   `yaml:"grafana"`
	Kubectl      Tool     `yaml:"kubectl"`
	KubectlCNPG  Tool     `yaml:"kubectl-cnpg"`
}

type Tool struct {
	Version string `yaml:"version"`
	SHA256  string `yaml:"sha256"`
}

// An exact chart version, not a range, which helm would resolve to a newer
// chart.
var exactVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// Parse reads a versions file and checks that each chart has an exact
// version and each image of the cluster a digest.
func Parse(data []byte) (*Manifest, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var m Manifest
	if err := doc.Decode(&m); err != nil {
		return nil, err
	}
	var errs []error
	for name, c := range m.Charts {
		if c.Name == "" || c.Repository == "" || !exactVersion.MatchString(c.Version) {
			errs = append(errs, fmt.Errorf("charts.%s needs name, repository and an exact version", name))
		}
	}
	sites, unpinned := imageSites(&doc)
	for _, s := range sites {
		m.Pinned = append(m.Pinned, s.pin.Repository+"@"+s.pin.Digest)
	}
	slices.Sort(m.Pinned)
	m.Pinned = slices.Compact(m.Pinned)
	for _, path := range unpinned {
		errs = append(errs, fmt.Errorf("the image %s has no digest", path))
	}
	return &m, errors.Join(errs...)
}

// Check fails on each lab pin that is missing, by its name.
func (l Lab) Check() error {
	var errs []error
	for name, value := range map[string]string{
		"node": l.Node, "controller": l.Controller, "ansible-core": l.AnsibleCore, "vm": l.VM,
		"vm-disk": l.VMDisk.SHA256, "dns": l.DNS, "acme": l.ACME, "s3": l.S3, "otel": l.OTel,
		"prometheus": l.Prometheus, "alertmanager": l.Alertmanager, "grafana": l.Grafana,
		"kubectl": l.Kubectl.SHA256, "kubectl-cnpg": l.KubectlCNPG.SHA256,
	} {
		if value == "" {
			errs = append(errs, fmt.Errorf("no lab.%s in %s", name, File))
		}
	}
	return errors.Join(errs...)
}

// Pin is an image or chart: its repository in full, its tag or version,
// and the image's digest.
type Pin struct {
	Repository, Tag, Digest string
}

// site is where the file pins an image or a chart: the scalars of its tag
// and of its digest, the same one when a string holds both, after prefix.
type site struct {
	pin         Pin
	tag, digest *yaml.Node
	prefix      string
}

// imageSites walks the file for the images of the cluster: each string such
// as repository:tag@sha256:…, and each block of a chart's values with a
// repository, after registry when there is one, and a digest in digest or
// after its tag's @. k3s's images go by tag, charts by version, and lab is
// not the cluster. It returns the paths of the blocks without a digest too.
func imageSites(doc *yaml.Node) (sites []site, unpinned []string) {
	var visit func(n *yaml.Node, path string)
	visit = func(n *yaml.Node, path string) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				visit(c, path)
			}
		case yaml.ScalarNode:
			if name, digest, ok := strings.Cut(n.Value, "@"); ok && strings.HasPrefix(digest, "sha256:") {
				t := tag(name)
				sites = append(sites, site{
					pin: Pin{Repository: Repository(name), Tag: t, Digest: digest},
					tag: n, digest: n, prefix: strings.TrimSuffix(name, t),
				})
			}
		case yaml.MappingNode:
			f := fields(n)
			if repo := scalar(f["repository"]); repo != "" {
				if registry := scalar(f["registry"]); registry != "" {
					repo = registry + "/" + repo
				}
				digestNode := f["digest"]
				tag, digest, _ := strings.Cut(scalar(f["tag"]), "@")
				if digest == "" {
					digest = scalar(digestNode)
				} else {
					digestNode = f["tag"]
				}
				if strings.HasPrefix(digest, "sha256:") {
					sites = append(sites, site{pin: Pin{Repository: Repository(repo), Tag: tag, Digest: digest}, tag: f["tag"], digest: digestNode})
				} else {
					unpinned = append(unpinned, path)
				}
			}
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i].Value
				if k == "tag" || (path == "" && (k == "k3s" || k == "charts" || k == "lab")) {
					continue
				}
				visit(n.Content[i+1], strings.TrimPrefix(path+"."+k, "."))
			}
		}
	}
	visit(doc, "")
	return sites, unpinned
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func fields(n *yaml.Node) map[string]*yaml.Node {
	f := map[string]*yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		f[n.Content[i].Value] = n.Content[i+1]
	}
	return f
}

// Update is the versions file with each image and chart moved to the Pin
// move gives for it, with every comment and line kept. A chart's Pin has
// its repository/name and its version as the tag. move runs once per pin.
func Update(data []byte, move func(Pin) (Pin, error)) ([]byte, error) {
	if _, err := Parse(data); err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("an empty versions file")
	}
	sites, _ := imageSites(&doc)
	charts := fields(doc.Content[0])["charts"]
	if charts == nil {
		return nil, errors.New("no charts")
	}
	for i := 1; i < len(charts.Content); i += 2 {
		f := fields(charts.Content[i])
		repo := strings.TrimPrefix(scalar(f["repository"]), "oci://") + "/" + scalar(f["name"])
		sites = append(sites, site{pin: Pin{Repository: repo, Tag: scalar(f["version"])}, tag: f["version"]})
	}
	moved := map[Pin]Pin{}
	var edits []edit
	for _, s := range sites {
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
			return nil, fmt.Errorf("%s has no tag to move to %s", s.pin.Repository, to.Tag)
		default:
			if to.Digest != s.pin.Digest {
				edits = append(edits, edit{s.digest, to.Digest})
			}
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

// Repository is the full form of an image name without its tag, with
// docker.io and library/ where the name leaves them out, as containerd
// writes them.
func Repository(name string) string {
	// A tag is after the last colon, unless that colon is in host:port.
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

func tag(name string) string {
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		return name[i+1:]
	}
	return ""
}

// Reference is the full form of an image reference by tag, such as
// docker.io/rancher/mirrored-pause:3.10.2.
func Reference(ref string) string {
	if t := tag(ref); t != "" {
		return Repository(ref) + ":" + t
	}
	return Repository(ref)
}
