package e2e

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
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
	imported := []string{m.K3s.Images[0], "docker.io/rancher/x@sha256:9"}
	got := notInManifest(images, imported, []*versions.Manifest{m})
	want := []string{
		m.K3s.Images[1] + " docker.io/rancher/y@sha256:6 (sha256:5)",
		"docker.io/library/busybox:1.37 docker.io/library/busybox@sha256:8 (sha256:3)",
		versions.Repository(pg) + "@sha256:7 (sha256:4)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	// After an image upgrade from a versions file whose instance image has
	// the digest sha256:7, the nodes may still have that image.
	data, err := os.ReadFile(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	before, err := versions.Parse([]byte(strings.Replace(string(data), pgDigest, "sha256:"+strings.Repeat("7", 64), 1)))
	if err != nil {
		t.Fatal(err)
	}
	images[4].RepoDigests = []string{versions.Repository(pg) + "@sha256:" + strings.Repeat("7", 64)}
	got = notInManifest(images, imported, []*versions.Manifest{before, m})
	if !slices.Equal(got, want[:2]) {
		t.Errorf("with the versions file before: got %q, want %q", got, want[:2])
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
	ms := []*versions.Manifest{m}
	if got := pulledAirgap(log, ms); !slices.Equal(got, []string{m.K3s.Images[0]}) {
		t.Errorf("got %q", got)
	}
	if got := pulledAirgap("", ms); got != nil {
		t.Errorf("no pull: got %q", got)
	}
}
