package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/images"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
	moby "github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The versions file of the checkout.
const versionsFile = "../versions/versions.yaml"

var scenarioFlag = flag.String("scenario", "", "the scenario to run: its tag, without the @")

// TestScenario runs one scenario:
//
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario <scenario>
//
// It builds e2e/Dockerfile from this checkout for the machine's own
// architecture, with the tools of the versions file, copies the k3s files,
// feature files and the versions file in and runs the
// image as one privileged container. Its last step copies the
// run's files out to results/. testcontainers removes the container and its
// volumes when the test ends, and Ryuk does when the test is killed. Without
// -scenario it skips, so go test ./... starts no container. The scenarios of
// labScenarios run on the machine's own Docker instead. A scenario readies
// the machine's kernel (prepareHost) before it starts k3s.
func TestScenario(t *testing.T) {
	if *scenarioFlag == "" {
		t.Skip("no -scenario")
	}
	m, err := versions.Load(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	if scenario, ok := labScenarios[*scenarioFlag]; ok {
		deadline, ok := t.Deadline()
		if !ok {
			t.Fatal("a run needs go test's -timeout")
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline.Add(-time.Minute))
		t.Cleanup(cancel)
		scenario(ctx, t, m)
		return
	}
	node, err := images.Get(t.Context(), "node")
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareHost(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	t.Logf("the versions file %s: k3s %s", versionsFile, m.K3s.Version)
	// testcontainers puts a directory under the parent of ContainerFilePath
	// with its own name, so each name is the same on both sides.
	files := []testcontainers.ContainerFile{
		{HostFilePath: "../ansible/k3s", ContainerFilePath: k3sDir, FileMode: 0o755},
		{HostFilePath: "features", ContainerFilePath: featuresDir, FileMode: 0o755},
		{HostFilePath: versionsFile, ContainerFilePath: manifestFile, FileMode: 0o644},
	}
	for _, f := range files {
		if err := noSymlinks(f.HostFilePath); err != nil {
			t.Fatal(err)
		}
	}
	deadline, ok := t.Deadline()
	if !ok {
		t.Fatal("a run needs go test's -timeout")
	}
	// The image build and the scenario share one budget, which leaves 5 min
	// before go test's -timeout for the copy-out of the results. Cleanups run
	// last first, so ctx ends after the container's cleanups, and the log
	// consumer reads to the end.
	ctx, cancel := context.WithDeadline(context.Background(), deadline.Add(-5*time.Minute))
	t.Cleanup(cancel)
	c, err := testcontainers.Run(ctx, "",
		testcontainers.WithDockerfile(testcontainers.FromDockerfile{
			Context: "..", Dockerfile: "e2e/Dockerfile", BuildLogWriter: os.Stdout, BuildArgs: toolArgs(m.Tools),
			// Kept, so that the next build reuses its layers.
			Repo: "cnpg-e2e-runner", Tag: "local", KeepImage: true,
		}),
		testcontainers.WithFiles(files...),
		testcontainers.WithHostConfigModifier(func(h *moby.HostConfig) { h.Privileged = true }),
		testcontainers.WithCmd(*scenarioFlag),
		testcontainers.WithLogConsumers(&testcontainers.StdoutLogConsumer{}),
		// ctx ends the wait first.
		testcontainers.WithWaitStrategyAndDeadline(time.Until(deadline), wait.ForExit()),
	)
	testcontainers.CleanupContainer(t, c)
	if c != nil {
		// Cleanups run last first, so this one runs before the container goes.
		t.Cleanup(func() { copyResults(t, c.GetContainerID()) })
	}
	if err != nil {
		t.Fatal(err)
	}
	// ctx can have ended just after the container exited.
	inspect, cancelInspect := context.WithTimeout(context.Background(), time.Minute)
	defer cancelInspect()
	state, err := c.State(inspect)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode != 0 {
		t.Fatalf("the scenario %s exited with %d: 1 when it failed, 2 when it could not run", *scenarioFlag, state.ExitCode)
	}
}

// labScenarios are the scenarios that run on the machine's own Docker, by
// their names.
var labScenarios = map[string]func(context.Context, *testing.T, *versions.Manifest){
	airgapScenario:  testAirgap,
	cacheScenario:   testCache,
	restoreScenario: testRestore,
	installScenario: func(ctx context.Context, t *testing.T, m *versions.Manifest) {
		testInstall(ctx, t, m, false)
	},
	installKVMScenario: func(ctx context.Context, t *testing.T, m *versions.Manifest) {
		testInstall(ctx, t, m, true)
	},
}

// hostSetup loads br_netfilter, which the nodes need and cannot load
// themselves, and raises fs.inotify.max_user_instances to 1024: the limit
// is the machine's, for every container alike, many machines have 128, and
// each node takes many.
const hostSetup = `set -eu
modprobe br_netfilter
have=$(sysctl -n fs.inotify.max_user_instances)
if [ "$have" -lt 1024 ]; then
  sysctl -w fs.inotify.max_user_instances=1024
fi`

// prepareHost runs hostSetup with the machine's own modprobe and sysctl,
// from a privileged container in the machine's mount namespace, so a run
// needs nothing on the machine besides Go and Docker. The changes hold until
// the machine reboots. node is an image of the nodes, which has nsenter: a
// fresh one, or a node of the cache.
func prepareHost(ctx context.Context, node string) error {
	_, err := command(ctx, "docker", "run", "--rm", "--privileged", "--pid=host", node,
		"nsenter", "--target", "1", "--mount", "--", "sh", "-c", hostSetup)
	return err
}

// toolArgs are the build arguments of e2e/Dockerfile with the tools of the
// versions file.
func toolArgs(t versions.Tools) map[string]*string {
	return map[string]*string{
		"KUBECTL_VERSION":  &t.Kubectl.Version,
		"KUBECTL_SHA256":   ptr(t.Kubectl.SHA256["amd64"]),
		"CNPG_VERSION":     &t.KubectlCNPG.Version,
		"CNPG_SHA256":      ptr(t.KubectlCNPG.SHA256["amd64"]),
		"ROTATE_CA_URL":    &t.RotateCA.URL,
		"ROTATE_CA_SHA256": &t.RotateCA.SHA256,
	}
}

func ptr(s string) *string { return &s }

// noSymlinks fails on a symlink in root: testcontainers would leave it out of
// the copy without a word.
func noSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; the run needs real files", path)
		}
		return err
	})
}

// copyResults copies the run's files out of the container to results/.
func copyResults(t *testing.T, id string) {
	out, err := exec.Command("docker", "cp", id+":"+resultsDir+"/.", "../results").CombinedOutput()
	if err != nil {
		t.Errorf("copying the results out: %v: %s", err, out)
	}
}
