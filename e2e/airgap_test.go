package main

import (
	"context"
	"net"
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
// every image of the versions file.
func testAirgap(ctx context.Context, t *testing.T, m *versions.Manifest) {
	l, err := startLab(ctx, t, m)
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
	for i, node := range l.nodes {
		code, out, err := execIn(ctx, node, "ip", "route", "show", "default")
		if err != nil || code != 0 || !strings.Contains(out, "via "+gatewayIP+" ") {
			t.Errorf("%s has no default route via %s: %d %v %s", nodeName(i), gatewayIP, code, err, out)
		}
		// curl exits with 7 when it cannot connect and with 28 when it
		// times out.
		code, out, err = execIn(ctx, node, "curl", "--silent", "--show-error", "--max-time", "10", "https://"+internet)
		if err != nil || (code != 7 && code != 28) {
			t.Errorf("%s reaches the internet, or curl failed otherwise: %d %v %s", nodeName(i), code, err, out)
		}
	}
	code, out, err := execIn(ctx, l.controller, "ansible", "nodes", "-m", "ping")
	if err != nil || code != 0 {
		for i, node := range l.nodes {
			_, journal, err := execIn(ctx, node, "journalctl", "--unit", "sshd", "--lines", "30", "--no-pager")
			t.Logf("sshd on %s: %v\n%s", nodeName(i), err, journal)
		}
		t.Fatalf("the controller does not reach every node over SSH: %d %v\n%s", code, err, out)
	}
	t.Logf("ansible nodes -m ping:\n%s", out)

	logf("filling the registry with every image of the versions file")
	if err := registry.Fill(ctx, registry.Client(registryAddr), m, registryAddr); err != nil {
		t.Fatal(err)
	}
	logf("the registry has the %d images", len(m.Pinned()))
}
