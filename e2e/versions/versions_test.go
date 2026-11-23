package versions

import (
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestRepository(t *testing.T) {
	for name, want := range map[string]string{
		"busybox":                      "docker.io/library/busybox",
		"busybox:1.37":                 "docker.io/library/busybox",
		"otel/opentelemetry-collector": "docker.io/otel/opentelemetry-collector",
		"docker.io/rancher/k3s:v1":     "docker.io/rancher/k3s",
		"docker.io/busybox":            "docker.io/library/busybox",
		"ghcr.io/cloudnative-pg/cloudnative-pg:1.30.1": "ghcr.io/cloudnative-pg/cloudnative-pg",
		"localhost:5000/app:1":                         "localhost:5000/app",
		"registry.example:5000/app":                    "registry.example:5000/app",
	} {
		if got := Repository(name); got != want {
			t.Errorf("Repository(%q) = %q, want %q", name, got, want)
		}
	}
	if got := Reference("rancher/mirrored-pause:3.10.2"); got != "docker.io/rancher/mirrored-pause:3.10.2" {
		t.Errorf("Reference: %q", got)
	}
}

const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

func TestImages(t *testing.T) {
	data, err := yaml.Marshal(map[string]any{
		"postgresql": map[string]any{"imageName": "registry.example/pg:18@" + digest},
		"chart": map[string]any{
			"image":   map[string]any{"repository": "quay.io/a/b", "tag": "v1", "digest": digest},
			"inTag":   map[string]any{"registry": "ghcr.io", "repository": "c/d", "tag": "v2@" + digest},
			"byTag":   map[string]any{"repository": "quay.io/e/f", "tag": "v3"},
			"list":    []any{"busybox@" + digest, "busybox:1"},
			"comment": "not an image",
		},
		"charts": map[string]any{"c": map[string]any{"repository": "https://charts.example", "version": "1.0.0"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var w images
	w.visit(&doc, "")
	if want := []string{"chart.byTag"}; !slices.Equal(w.unpinned, want) {
		t.Errorf("unpinned: got %q, want %q", w.unpinned, want)
	}
	var got []string
	for _, s := range w.sites {
		got = append(got, s.pin.String())
	}
	slices.Sort(got)
	want := []string{
		"docker.io/library/busybox:@" + digest,
		"ghcr.io/c/d:v2@" + digest,
		"quay.io/a/b:v1@" + digest,
		"registry.example/pg:18@" + digest,
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

const file = "../../versions/versions.yaml"

// The versions file has what a run reads. Pinned has its registry and
// PostgreSQL images, and every image and chart but k3s's is one of this
// repo's packages.
func TestVersionsFile(t *testing.T) {
	m, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{m.Registry.Image, m.PostgreSQL.ImageName} {
		name, d, _ := strings.Cut(image, "@")
		if !slices.Contains(m.Pinned(), Repository(name)+"@"+d) {
			t.Errorf("%s is not in Pinned", image)
		}
	}
	for _, p := range m.Images() {
		if !strings.HasPrefix(p.Repository, Packages+"/") || p.Tag == "" {
			t.Errorf("%s is not one of this repo's packages by tag", p)
		}
	}
	for name, c := range m.Charts {
		if c.Repository != "oci://"+Packages {
			t.Errorf("the chart %s is in %s", name, c.Repository)
		}
	}
}

func TestCheck(t *testing.T) {
	if _, err := Parse([]byte("k3s: {version: v1}\n")); err == nil || !strings.Contains(err.Error(), "no k3s.images") {
		t.Errorf("a manifest with only k3s.version: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// A chart without its name, and a uid without a gid.
	half := strings.Replace(string(data), "    name: cert-manager-chart\n", "", 1)
	half = strings.Replace(half, "  postgresGID: 70\n", "", 1)
	if _, err := Parse([]byte(half)); err == nil || !strings.Contains(err.Error(), "no charts.cert-manager.name") ||
		!strings.Contains(err.Error(), "no postgresql.postgresUID and postgresGID, both above 0") {
		t.Errorf("a chart without its name and a uid without a gid: %v", err)
	}
	// The agent's image without its digest, as a tag only.
	agent := strings.Index(string(data), "\nagent:")
	noDigest := string(data[:agent]) + strings.Replace(string(data[agent:]), "    digest: sha256:", "    # digest: sha256:", 1)
	if _, err := Parse([]byte(noDigest)); err == nil || !strings.Contains(err.Error(), "the image agent.image has no digest") {
		t.Errorf("a chart image without a digest: %v", err)
	}
	// The ACME solver's image left out: the chart would run its own.
	solver := strings.Index(string(data), "  acmesolver:\n")
	end := strings.Index(string(data)[solver:], "  startupapicheck:")
	if _, err := Parse([]byte(string(data[:solver]) + string(data[solver+end:]))); err == nil ||
		!strings.Contains(err.Error(), "no image cert-manager.acmesolver.image with a repository and its digest in digest") {
		t.Errorf("a chart image left out: %v", err)
	}
	// The solver's block under a literal dotted key, which Helm does not read.
	dotted := string(data[:solver]) + string(data[solver+end:]) +
		"\"cert-manager.acmesolver.image\":\n  registry: ghcr.io\n  repository: djosh34/cnpg-e2e/cert-manager-acmesolver\n  digest: sha256:" + strings.Repeat("0", 64) + "\n"
	if _, err := Parse([]byte(dotted)); err == nil || !strings.Contains(err.Error(), "no image cert-manager.acmesolver.image") {
		t.Errorf("a chart image under a dotted key: %v", err)
	}
	// A second digest next to the sidecar's tag, which its chart ignores.
	twoDigests := strings.Replace(string(data), "    repository: djosh34/cnpg-e2e/cloudnative-pg-plugin-barman-cloud-sidecar\n",
		"    repository: djosh34/cnpg-e2e/cloudnative-pg-plugin-barman-cloud-sidecar\n    digest: sha256:"+strings.Repeat("1", 64)+"\n", 1)
	if _, err := Parse([]byte(twoDigests)); err == nil ||
		!strings.Contains(err.Error(), "the image plugin-barman-cloud.sidecarImage has a digest in digest and in tag") {
		t.Errorf("an image with two digests: %v", err)
	}
	// The solver's block without its repository.
	noRepo := strings.Replace(string(data), "      repository: djosh34/cnpg-e2e/cert-manager-acmesolver\n", "", 1)
	if _, err := Parse([]byte(noRepo)); err == nil || !strings.Contains(err.Error(), "no image cert-manager.acmesolver.image with a repository") {
		t.Errorf("a chart image without its repository: %v", err)
	}
	// CNPG's digest in a digest field, which its chart does not read.
	cnpgAt := strings.Index(string(data), "\ncloudnative-pg:\n")
	if cnpgAt < 0 {
		t.Fatal("the versions file has no top-level cloudnative-pg key")
	}
	cnpg := string(data[:cnpgAt]) + strings.Replace(string(data[cnpgAt:]), `@sha256:`, `"
    digest: "sha256:`, 1)
	if _, err := Parse([]byte(cnpg)); err == nil || !strings.Contains(err.Error(), "no image cloudnative-pg.image with a repository and its digest in tag") {
		t.Errorf("CNPG's digest outside its tag: %v", err)
	}
	for version, exact := range map[string]bool{"0.29.1": true, "v1.21.3": true, "1.0.0-rc.1": true, "0.29.x": false, "~0.29.1": false, ">=1.0.0": false, "0.29": false} {
		if exactVersion.MatchString(version) != exact {
			t.Errorf("%s: exact %v", version, !exact)
		}
	}
}

// Update moves each pin of the versions file where move says, in place,
// and leaves every other line as it is.
func TestUpdate(t *testing.T) {
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	same, err := Update(data, func(p Pin) (Pin, error) { return p, nil })
	if err != nil || string(same) != string(data) {
		t.Fatalf("Update with no move changed the file: %v", err)
	}
	newDigest := func(n string) string { return "sha256:" + strings.Repeat(n, 64) }
	moves := map[string]Pin{
		"cert-manager-chart":      {Tag: "1.22.0", Digest: newDigest("a")},
		"cert-manager-controller": {Tag: "1.22.0", Digest: newDigest("b")},
		"postgres":                {Tag: "18.8-debian13", Digest: newDigest("c")},
		"cloudnative-pg":          {Tag: "1.30.3", Digest: newDigest("d")},
		"opentelemetry-collector": {Tag: "0.166.0-contrib", Digest: newDigest("e")},
	}
	calls := map[Pin]int{}
	out, err := Update(data, func(p Pin) (Pin, error) {
		calls[p]++
		if to, ok := moves[strings.TrimPrefix(p.Repository, Packages+"/")]; ok {
			return Pin{Repository: p.Repository, Tag: to.Tag, Digest: to.Digest}, nil
		}
		return p, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for p, n := range calls {
		if n != 1 {
			t.Errorf("move of %s: %d calls", p, n)
		}
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if c := m.Charts["cert-manager"]; c.Version != "1.22.0" || c.Digest != newDigest("a") {
		t.Errorf("the cert-manager chart: %+v", c)
	}
	for _, want := range []string{
		Packages + "/cert-manager-controller:1.22.0@" + newDigest("b"),
		Packages + "/postgres:18.8-debian13@" + newDigest("c"),
		Packages + "/cloudnative-pg:1.30.3@" + newDigest("d"),
		Packages + "/opentelemetry-collector:0.166.0-contrib@" + newDigest("e"),
	} {
		if !slices.ContainsFunc(m.Images(), func(p Pin) bool { return p.String() == want }) {
			t.Errorf("no image %s", want)
		}
	}
	// The chart's version and digest, the controller's tag and digest, the
	// postgres and CNPG strings, and each collector's tag and digest.
	before, after := strings.Split(string(data), "\n"), strings.Split(string(out), "\n")
	if len(before) != len(after) {
		t.Fatalf("%d lines, then %d", len(before), len(after))
	}
	changed := 0
	for i := range before {
		if before[i] != after[i] {
			changed++
		}
	}
	if changed != 10 {
		t.Errorf("%d lines changed, want 10:\n%s", changed, out)
	}
}
