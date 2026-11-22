// Command e2e runs one scenario of the feature files in /features on a new
// three-node k3s cluster, all inside this one privileged container, and
// prints each step with its time:
//
//	e2e <scenario>
//
// The scenario is the tag of one scenario, without the @. The cluster is
// built from the charts and values in /helm and the k3s files in /k3s, with
// every version from the versions file /versions.yaml. TestScenario of
// scenario_test.go copies them from the checkout before the container
// starts; the run never writes into them. The monitoring files are those of
// e2e/images, built into the runner.
// The run writes its files into a new directory of /results, which
// TestScenario copies out at the end. The exit code is 0 when the scenario
// passed, 1 when it failed, and 2 when it could not run.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// Where TestScenario puts the checkout's files in the container.
const (
	// helm/: charts/, releases.yaml and values/.
	helmDir = "/helm"
	// ansible/k3s/.
	k3sDir = "/k3s"
	// e2e/features/.
	featuresDir = "/features"
	// versions/versions.yaml.
	manifestFile = "/versions.yaml"
	// Where a run keeps its own files: the copied charts, the kubeconfig and
	// helm's caches.
	workDir = "/work"
)

// manifest holds the versions of the run. run reads it once, before the
// cluster is built.
var manifest *versions.Manifest

func main() {
	if len(os.Args) != 2 {
		say(os.Stderr, "usage: e2e <scenario>\n")
		os.Exit(2)
	}
	os.Exit(run(os.Args[1]))
}

func run(scenario string) int {
	if err := checkInotify(); err != nil {
		say(os.Stderr, "e2e: %v\n", err)
		return 2
	}
	if err := checkBrNetfilter(); err != nil {
		say(os.Stderr, "e2e: %v\n", err)
		return 2
	}
	if !scenarioTag.MatchString(scenario) {
		say(os.Stderr, "e2e: %q is not a scenario tag\n", scenario)
		return 2
	}
	results, err := newResults(scenario)
	if err != nil {
		say(os.Stderr, "e2e: results: %v\n", err)
		return 2
	}
	if manifest, err = versions.Load(manifestFile); err != nil {
		say(os.Stderr, "e2e: the versions file: %v\n", err)
		return 2
	}
	// The results say which versions ran.
	if err := copyFile(manifestFile, results.path("versions.yaml")); err != nil {
		say(os.Stderr, "e2e: %v\n", err)
		return 2
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		say(os.Stderr, "e2e: %v\n", err)
		return 2
	}
	if err := startDocker(context.Background()); err != nil {
		say(os.Stderr, "e2e: %v\n", err)
		return 2
	}
	disks := &dataDisks{}
	code := 2
	if err := disks.prepare(context.Background()); err != nil {
		say(os.Stderr, "e2e: the nodes' data filesystems: %v\n", err)
	} else {
		disks.watch()
		code = runScenario(scenario, results)
	}
	// A run that cannot clean up is a fault of the run, also when its
	// scenario passed.
	if err := disks.finish(context.Background(), results); err != nil {
		say(os.Stderr, "e2e: cleaning up the nodes' data filesystems: %v\n", err)
		code = max(code, 2)
	}
	return code
}

// A run needs this many inotify instances. The limit is the host's, for
// every container and user alike, and each k3s node takes many.
const inotifyNeeded = 1024

func checkInotify() error {
	const file = "/proc/sys/fs/inotify/max_user_instances"
	var limit int
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if _, err := fmt.Sscan(string(data), &limit); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if limit < inotifyNeeded {
		return fmt.Errorf("the host's fs.inotify.max_user_instances is %d; the three k3s nodes need at least %d. "+
			"TestScenario raises it, or: sudo sysctl -w fs.inotify.max_user_instances=%d",
			limit, inotifyNeeded, inotifyNeeded)
	}
	return nil
}

// checkBrNetfilter checks that the host's kernel has br_netfilter, built in
// or loaded, so that no iptables rule of a node makes the kernel load it
// while the cluster starts.
func checkBrNetfilter() error {
	_, err := os.Stat("/proc/sys/net/bridge/bridge-nf-call-iptables")
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("the host's kernel has not loaded br_netfilter, which the k3s nodes need. " +
			"Load it on the host with: sudo modprobe br_netfilter")
	}
	return err
}

var errNoScenario = errors.New("no scenario has that tag")

func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644)
}
