package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/registry"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// The scenario that checks the lab, run like any other:
//
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario airgap
const airgapScenario = "airgap"

// An address on the internet that answers on 443.
const internet = "1.1.1.1:443"

// testAirgap starts the lab, then checks that no node reaches the internet
// while the machine does, and that the controller reaches every node over
// SSH with Ansible. Last, it fills the registry, and so the store, with
// every image of the versions file. It writes what it saw to
// results/airgap/report.txt.
func testAirgap(ctx context.Context, t *testing.T, m *versions.Manifest) {
	l, err := startLab(ctx, t, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	// So a node that cannot is the network's doing.
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", internet)
	if err != nil {
		t.Fatalf("this machine does not reach the internet: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	// The same TCP connection from each node: bash exits with 1 when it
	// cannot connect, and timeout with 124 when it does not within 10 s.
	connect := "exec 3<>/dev/tcp/" + strings.Replace(internet, ":", "/", 1)
	for i, node := range l.nodes {
		code, out, err := execIn(ctx, node, []string{"timeout", "10", "bash", "-c", connect})
		if err != nil || (code != 1 && code != 124) {
			t.Errorf("%s reaches the internet, or the check failed otherwise: %d %v %s", nodeName(i), code, err, out)
		}
	}
	code, out, err := execIn(ctx, l.controller, []string{"ansible", "nodes", "-m", "ping"})
	if err != nil || code != 0 {
		for i, node := range l.nodes {
			_, journal, err := execIn(ctx, node, []string{"journalctl", "--unit", "sshd", "--lines", "30", "--no-pager"})
			t.Logf("sshd on %s: %v\n%s", nodeName(i), err, journal)
		}
		t.Fatalf("the controller does not reach every node over SSH: %d %v\n%s", code, err, out)
	}
	report := "ansible nodes -m ping:\n" + out
	t.Logf("%s", report)

	logf("filling the registry with every image of the versions file")
	if err := registry.Fill(ctx, registry.Client(registryAddr), m, registryAddr); err != nil {
		t.Fatal(err)
	}
	logf("the registry has the %d images", len(m.Pinned()))
	report += "\nThe images in the registry:\n" + strings.Join(m.Pinned(), "\n") + "\n"
	if err := os.MkdirAll("../results/airgap", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../results/airgap/report.txt", []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
}
