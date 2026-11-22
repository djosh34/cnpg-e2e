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
// architecture, with the tools of the versions file, copies the charts,
// values, k3s files, monitoring files, feature files and the versions file in
// and runs the image as one privileged container. Its last step copies the
// run's files out to results/. testcontainers removes the container and its
// volumes when the test ends, and Ryuk does when the test is killed. Without
// -scenario it skips, so go test ./... starts no container.
func TestScenario(t *testing.T) {
	if *scenarioFlag == "" {
		t.Skip("no -scenario")
	}
	m, err := versions.Load(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the versions file %s: k3s %s", versionsFile, m.K3s.Version)
	// testcontainers puts a directory under the parent of ContainerFilePath
	// with its own name, so each name is the same on both sides.
	files := []testcontainers.ContainerFile{
		{HostFilePath: "../helm", ContainerFilePath: helmDir, FileMode: 0o755},
		{HostFilePath: "../ansible/k3s", ContainerFilePath: k3sDir, FileMode: 0o755},
		{HostFilePath: "monitoring", ContainerFilePath: monitoringDir, FileMode: 0o755},
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
	// before go test's -timeout for the copy-out of the results.
	ctx, cancel := context.WithDeadline(context.Background(), deadline.Add(-5*time.Minute))
	defer cancel()
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
	state, err := c.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode != 0 {
		t.Fatalf("the scenario %s exited with %d: 1 when it failed, 2 when it could not run", *scenarioFlag, state.ExitCode)
	}
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
