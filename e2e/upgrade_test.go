package e2e

import (
	"context"
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
		]}},
		{"metadata": {"namespace": "kube-system", "name": "coredns"}, "status": {"containerStatuses": [
			{"name": "coredns", "image": "` + m.K3s.Images[3] + `", "imageID": "docker.io/rancher/mirrored-coredns-coredns@sha256:9", "state": {"running": {}}}
		]}}
	]}`
	err = containersOn(pods, m)
	if err == nil || !strings.Contains(err.Error(), "the container old of databases/pg-1") || strings.Count(err.Error(), "\n") != 0 {
		t.Errorf("got %v, want only the container old", err)
	}
}

func TestFirstOther(t *testing.T) {
	data, err := os.ReadFile(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	m, err := versions.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	_, pgDigest, _ := strings.Cut(m.PostgreSQL.ImageName, "@")
	other := []byte(strings.Replace(string(data), pgDigest, "sha256:"+strings.Repeat("7", 64), 1))
	source := func(name string, data []byte) versionsSource {
		return versionsSource{name: name, read: func(context.Context) ([]byte, error) { return data, nil }}
	}
	name, got, err := firstOther(context.Background(), m, []versionsSource{
		source("the same", data), source("other", other), source("later", other),
	})
	if err != nil || name != "other" || string(got) != string(other) {
		t.Errorf("got %q, %v", name, err)
	}
	if _, _, err := firstOther(context.Background(), m, []versionsSource{source("the same", data)}); err == nil {
		t.Error("only the same images: want an error")
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
}
