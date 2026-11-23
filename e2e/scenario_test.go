package e2e

import (
	"context"
	"flag"
	"testing"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

var scenarioFlag = flag.String("scenario", "", "the scenario to run: its tag, without the @")

// TestScenario runs one scenario on the machine's own Docker:
//
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario <scenario>
//
// The scenario is one of labScenarios, or the tag of a scenario of the
// feature files, without the @ (feature.go). A scenario readies the
// machine's kernel (prepareHost) before it starts k3s. The run's files go
// to results/ of the checkout. Without -scenario it skips, so go test ./...
// starts no container. testcontainers removes every container when the test
// ends, and Ryuk does when the test is killed.
func TestScenario(t *testing.T) {
	if *scenarioFlag == "" {
		t.Skip("no -scenario")
	}
	m, err := versions.Load(versionsFile)
	if err != nil {
		t.Fatal(err)
	}
	deadline, ok := t.Deadline()
	if !ok {
		t.Fatal("a run needs go test's -timeout")
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline.Add(-time.Minute))
	t.Cleanup(cancel)
	if scenario, ok := labScenarios[*scenarioFlag]; ok {
		scenario(ctx, t, m)
		return
	}
	testFeature(ctx, t, m, *scenarioFlag)
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
