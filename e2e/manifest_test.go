package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"go.yaml.in/yaml/v3"
)

func TestNotInManifest(t *testing.T) {
	m, err := versions.Load(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	pg, pgDigest, _ := strings.Cut(m.PostgreSQL.ImageName, "@")
	images, err := parseImages(`{"images": [
		{"id": "sha256:1", "repoTags": ["` + m.K3s.Images[0] + `"], "repoDigests": ["docker.io/rancher/x@sha256:9"]},
		{"id": "sha256:5", "repoTags": ["` + m.K3s.Images[1] + `"], "repoDigests": ["docker.io/rancher/y@sha256:6"]},
		{"id": "sha256:2", "repoTags": [], "repoDigests": ["` + versions.Repository(pg) + "@" + pgDigest + `"]},
		{"id": "sha256:3", "repoTags": ["docker.io/library/busybox:1.37"], "repoDigests": ["docker.io/library/busybox@sha256:8"]},
		{"id": "sha256:4", "repoTags": [], "repoDigests": ["` + versions.Repository(pg) + `@sha256:7"]}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	// k3s imported the first airgap image, not the second.
	got := notInManifest(images, []string{m.K3s.Images[0], "docker.io/rancher/x@sha256:9"}, m)
	want := []string{
		m.K3s.Images[1] + " docker.io/rancher/y@sha256:6 (sha256:5)",
		"docker.io/library/busybox:1.37 docker.io/library/busybox@sha256:8 (sha256:3)",
		versions.Repository(pg) + "@sha256:7 (sha256:4)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPulledAirgap(t *testing.T) {
	m, err := versions.Load(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	log := `time="2026-11-19T10:14:31Z" level=info msg="stop pulling image docker.io/library/busybox:1.37: active requests=0, bytes read=1922381"
time="2026-11-19T10:14:54Z" level=info msg="stop pulling image ` + m.K3s.Images[0] + `: active requests=0, bytes read=15177"
time="2026-11-19T10:14:55Z" level=info msg="stop pulling image ` + m.K3s.Images[0] + `: active requests=0, bytes read=15177"
`
	if got := pulledAirgap(log, m); !slices.Equal(got, []string{m.K3s.Images[0]}) {
		t.Errorf("got %q", got)
	}
	if got := pulledAirgap("", m); got != nil {
		t.Errorf("no pull: got %q", got)
	}
}

func TestSetDependencies(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Chart.yaml")
	write := func(content string) {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	charts := map[string]versions.Chart{"otel": {Repository: "https://charts.example", Version: "1.2.3"}}
	write("name: wrapper\ndependencies:\n  - name: otel\n    alias: agent\n  - name: otel\n    alias: cluster\n")
	if err := setDependencies(file, charts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		Dependencies []map[string]string `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatal(err)
	}
	for _, d := range chart.Dependencies {
		if d["repository"] != "https://charts.example" || d["version"] != "1.2.3" || d["name"] != "otel" {
			t.Errorf("a dependency: %v", d)
		}
	}
	if len(chart.Dependencies) != 2 || chart.Dependencies[1]["alias"] != "cluster" {
		t.Errorf("the dependencies: %v", chart.Dependencies)
	}
	write("name: wrapper\ndependencies:\n  - name: other\n")
	if err := setDependencies(file, charts); err == nil {
		t.Error("a chart the manifest does not have: no error")
	}
}

// A chart of another name gets the manifest's name, and its alias unless
// the dependency has one of its own.
func TestSetDependenciesName(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Chart.yaml")
	content := "name: wrapper\ndependencies:\n  - name: otel\n  - name: otel\n    alias: agent\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	charts := map[string]versions.Chart{"otel": {Name: "otel-chart", Alias: "otel", Repository: "oci://registry.example/charts", Version: "1.2.3"}}
	if err := setDependencies(file, charts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		Dependencies []map[string]string `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatal(err)
	}
	var aliases []string
	for _, d := range chart.Dependencies {
		if d["name"] != "otel-chart" || d["repository"] != "oci://registry.example/charts" || d["version"] != "1.2.3" {
			t.Errorf("a dependency: %v", d)
		}
		aliases = append(aliases, d["alias"])
	}
	if !slices.Equal(aliases, []string{"otel", "agent"}) {
		t.Errorf("the aliases: %q", aliases)
	}
}

// Every wrapper chart depends only on charts the versions file has, and
// names no version itself.
func TestWrapperDependencies(t *testing.T) {
	m, err := versions.Load(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("../helm/charts/*/Chart.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no charts: %v", err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var chart struct {
			Dependencies []map[string]string `yaml:"dependencies"`
		}
		if err := yaml.Unmarshal(data, &chart); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, d := range chart.Dependencies {
			if _, ok := m.Charts[d["name"]]; !ok {
				t.Errorf("%s: %s has no chart in the versions file", file, d["name"])
			}
			if d["version"] != "" || d["repository"] != "" {
				t.Errorf("%s: %s names a version or repository; the versions file has them", file, d["name"])
			}
		}
	}
}
