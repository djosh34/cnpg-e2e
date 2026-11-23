package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
// filesystem of fixed size (disks.go). The image upgrade deploys an earlier
// versions file instead (upgradeFrom), which it writes to before.yaml. The e2e test then runs kubectl and
// kubectl-cnpg of the versions file on this machine, and the client in a
// container on the lab's network. It writes the run's files to
// results/<name>/.
func testFeature(ctx context.Context, t *testing.T, m *versions.Manifest, name string) {
	if err := isFeatureTag(name); err != nil {
		t.Fatal(err)
	}
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
	l, report, start := startCached(ctx, t, m)
	// Cleanups run last first, so this one runs before the lab's network
	// goes: a scenario that ran out of time in a store outage never removed
	// the store's stand-in.
	t.Cleanup(func() {
		remove, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := command(remove, "docker", "rm", "-f", storeGone); err != nil && !strings.Contains(err.Error(), "No such container") {
			t.Error(err)
		}
	})
	if name == diskScenario {
		if err := l.dataDisks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	first := m
	var before *versions.Manifest
	if name == imageUpgradeScenario {
		from, data, b, err := upgradeFrom(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		logf("the image upgrade starts from the versions file of %s", from)
		report += fmt.Sprintf("The image upgrade starts from the versions file of %s.\n", from)
		if err := errors.Join(res.write("before.yaml", string(data)), l.useVersions(ctx, data)); err != nil {
			t.Fatal(err)
		}
		first, before = b, b
	}
	deployed, env, err := l.deploy(ctx, first, nil, "upgrade.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := res.write("deploy.txt", report+"\n"+deployed); err != nil {
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
	w := &world{lab: l, m: m, before: before, env: env, mon: mon, built: start}
	if err := runScenario(ctx, w, name, res); err != nil {
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
	file := filepath.Join(dir, "kubeconfig")
	err = os.WriteFile(file, []byte(strings.ReplaceAll(config, "https://127.0.0.1:6443", "https://"+nodeIP(0)+":6443")), 0o600)
	if err != nil {
		return err
	}
	kubeconfig = file
	return nil
}

// A feature tag's line in a feature file.
var tagLine = regexp.MustCompile(`(?m)^\s*@([a-z0-9-]+)\s*$`)

// isFeatureTag fails when no scenario of the feature files has the tag, so
// a misspelt name starts no cluster.
func isFeatureTag(name string) error {
	files, err := filepath.Glob(filepath.Join(featuresDir, "*.feature"))
	if err != nil {
		return err
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		for _, m := range tagLine.FindAllStringSubmatch(string(data), -1) {
			if m[1] == name && name != "always" {
				return nil
			}
		}
	}
	return fmt.Errorf("%q is neither a scenario of the lab nor a tag of a scenario in %s", name, featuresDir)
}
