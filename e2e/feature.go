package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// testFeature runs the scenario of the feature files tagged @<name> as
// every scenario there starts: the lab from the cache of this checkout,
// pulled or built here, then the fill and upgrade.yml, which deploys every
// chart, and a healthy cluster with its first backup and the metrics of
// every source. The scenario data-disk-full first gives every node a data
// filesystem of fixed size (disks.go). The e2e test then runs kubectl and
// kubectl-cnpg of the versions file on this machine, and the client in a
// container on the lab's network. It writes the run's files to
// results/<name>/.
func testFeature(ctx context.Context, t *testing.T, m *versions.Manifest, name string) {
	res, err := newResults(name)
	if err != nil {
		t.Fatal(err)
	}
	// The results say which versions ran.
	data, err := os.ReadFile(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.write("versions.yaml", string(data)); err != nil {
		t.Fatal(err)
	}
	tag, err := cacheTag()
	if err != nil {
		t.Fatal(err)
	}
	from, err := getCache(ctx, t, m, tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareHost(ctx, from.images[nodeName(0)]); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	l, err := startLab(ctx, t, m, from, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ready(ctx, start); err != nil {
		t.Fatal(err)
	}
	if name == diskScenario {
		if err := l.dataDisks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	report, env, err := l.deploy(ctx, m, "upgrade.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := res.write("deploy.txt", report); err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	if err := installTools(ctx, m, tools); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := writeKubeconfig(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	logf("building the client image")
	if err := buildClient(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	mon, err := newMonitoring(l.root)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{lab: l, m: m, env: env, mon: mon, built: start}
	if err := runScenario(w, name, res); err != nil {
		t.Fatal(err)
	}
}

// writeKubeconfig writes the kubeconfig of k3s's admin into dir, at the API
// server of the first node, and points kubectl at it.
func writeKubeconfig(ctx context.Context, dir string) error {
	config, err := command(ctx, "docker", "exec", nodeName(0), "cat", "/etc/rancher/k3s/k3s.yaml")
	if err != nil {
		return err
	}
	if !strings.Contains(config, "https://127.0.0.1:6443") {
		return fmt.Errorf("the kubeconfig of %s has no server https://127.0.0.1:6443", nodeName(0))
	}
	file := filepath.Join(dir, "kubeconfig")
	err = os.WriteFile(file, []byte(strings.ReplaceAll(config, "https://127.0.0.1:6443", "https://"+nodeIP(0)+":6443")), 0o600)
	if err != nil {
		return err
	}
	kubeconfig = file
	return nil
}
