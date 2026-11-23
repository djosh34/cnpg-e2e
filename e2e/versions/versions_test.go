package versions

import (
	"os"
	"slices"
	"strings"
	"testing"
)

const file = "../../" + File

func TestRepository(t *testing.T) {
	for name, want := range map[string]string{
		"busybox":                      "docker.io/library/busybox",
		"busybox:1.37":                 "docker.io/library/busybox",
		"otel/opentelemetry-collector": "docker.io/otel/opentelemetry-collector",
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

// Every image and chart of the cluster is one of this repo's packages, and
// the lab is complete.
func TestVersionsFile(t *testing.T) {
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	name, digest, _ := strings.Cut(m.PostgreSQL.ImageName, "@")
	if !slices.Contains(m.Pinned, Repository(name)+"@"+digest) {
		t.Errorf("Pinned has no %s: %q", m.PostgreSQL.ImageName, m.Pinned)
	}
	for _, image := range m.Pinned {
		if !strings.HasPrefix(image, Packages+"/") {
			t.Errorf("%s is not one of this repo's packages", image)
		}
	}
	for name, c := range m.Charts {
		if c.Repository != "oci://"+Packages {
			t.Errorf("the chart %s is in %s", name, c.Repository)
		}
	}
	if err := m.Lab.Check(); err != nil {
		t.Error(err)
	}
}

func TestUnpinned(t *testing.T) {
	_, err := Parse([]byte("agent:\n  image:\n    repository: ghcr.io/a/b\n    tag: \"1\"\ncharts:\n  c: {name: c, repository: oci://r, version: 1.x}\n"))
	if err == nil || !strings.Contains(err.Error(), "the image agent.image has no digest") ||
		!strings.Contains(err.Error(), "charts.c needs name, repository and an exact version") {
		t.Errorf("got %v", err)
	}
}

// Update moves each pin where move says, in place, and leaves every other
// line, lab's included, as it is.
func TestUpdate(t *testing.T) {
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	same, err := Update(data, func(p Pin) (Pin, error) { return p, nil })
	if err != nil || string(same) != string(data) {
		t.Fatalf("Update with no move changed the file: %v", err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	calls := map[Pin]int{}
	out, err := Update(data, func(p Pin) (Pin, error) {
		calls[p]++
		if strings.HasPrefix(p.Repository, "docker.io") || !strings.HasPrefix(p.Repository, Packages) {
			t.Errorf("move of %s, which is not one of this repo's packages", p.Repository)
		}
		if p.Digest == "" {
			return Pin{Repository: p.Repository, Tag: p.Tag + "-new"}, nil
		}
		return Pin{Repository: p.Repository, Tag: "9.9", Digest: digest}, nil
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
	if c := m.Charts["cert-manager"]; c.Version != "1.21.3-new" {
		t.Errorf("the cert-manager chart: %+v", c)
	}
	if m.PostgreSQL.ImageName != Packages+"/postgres:9.9@"+digest {
		t.Errorf("postgresql.imageName: %s", m.PostgreSQL.ImageName)
	}
	before, after := strings.Split(string(data), "\n"), strings.Split(string(out), "\n")
	if len(before) != len(after) {
		t.Fatalf("%d lines, then %d", len(before), len(after))
	}
	lab := slices.Index(before, "lab:")
	for i := range before {
		if before[i] != after[i] && (i > lab || !strings.Contains(after[i], "9.9") && !strings.Contains(after[i], digest) && !strings.Contains(after[i], "-new")) {
			t.Errorf("line %d changed to %q", i+1, after[i])
		}
	}
}
