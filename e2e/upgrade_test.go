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
	name, got, before, err := firstOther(context.Background(), m, []versionsSource{
		source("the same", data), source("other", other), source("later", other),
	})
	if err != nil || name != "other" || string(got) != string(other) || before.PostgreSQL.ImageName == m.PostgreSQL.ImageName {
		t.Errorf("got %q, %v", name, err)
	}
	if _, _, _, err := firstOther(context.Background(), m, []versionsSource{source("the same", data)}); err == nil {
		t.Error("only the same images: want an error")
	}
}
