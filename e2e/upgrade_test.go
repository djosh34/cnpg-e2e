package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

func TestContainersOn(t *testing.T) {
	m, err := versions.Load(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	pg, pgDigest, _ := strings.Cut(m.PostgreSQL.ImageName, "@")
	pods := `{"items": [
		{"metadata": {"namespace": "databases", "name": "pg-1"}, "status": {"containerStatuses": [
			{"name": "postgres", "image": "` + m.PostgreSQL.ImageName + `", "imageID": "` + pg + "@" + pgDigest + `", "state": {"running": {}}},
			{"name": "old", "image": "` + pg + `", "imageID": "` + pg + `@sha256:7", "state": {"running": {}}},
			{"name": "ended", "image": "` + pg + `", "imageID": "` + pg + `@sha256:7", "state": {"terminated": {}}}
		], "initContainerStatuses": [
			{"name": "sidecar", "image": "` + pg + `", "imageID": "` + pg + `@sha256:8", "state": {"running": {}}}
		]}},
		{"metadata": {"namespace": "kube-system", "name": "coredns"}, "status": {"containerStatuses": [
			{"name": "coredns", "image": "` + m.K3s.Images[3] + `", "imageID": "docker.io/rancher/mirrored-coredns-coredns@sha256:9", "state": {"running": {}}}
		]}}
	]}`
	err = containersOn(pods, m)
	if err == nil || !strings.Contains(err.Error(), "the container old of databases/pg-1") ||
		!strings.Contains(err.Error(), "the container sidecar of databases/pg-1") || strings.Count(err.Error(), "\n") != 1 {
		t.Errorf("got %v, want the containers old and sidecar", err)
	}
}

func TestWithK3s(t *testing.T) {
	data, err := os.ReadFile(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	m, err := versions.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	_, pgDigest, _ := strings.Cut(m.PostgreSQL.ImageName, "@")
	earlier := strings.Replace(string(data), pgDigest, "sha256:"+strings.Repeat("7", 64), 1)
	earlier = strings.ReplaceAll(earlier, m.K3s.Version, "v1.0.0+k3s1")
	merged, err := withK3s([]byte(earlier), data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := versions.Parse(merged)
	if err != nil {
		t.Fatal(err)
	}
	if got.K3s.Version != m.K3s.Version || !strings.HasSuffix(got.PostgreSQL.ImageName, strings.Repeat("7", 64)) {
		t.Errorf("got k3s %s and %s", got.K3s.Version, got.PostgreSQL.ImageName)
	}
	for _, empty := range []string{"", "null\n"} {
		if _, err := withK3s([]byte(empty), data); err == nil {
			t.Errorf("%q: want an error", empty)
		}
	}
}
