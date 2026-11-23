package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/images"
	"github.com/djosh34/cnpg-e2e/e2e/registry"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// The scenario that installs everything on empty nodes, as production
// does on new machines, and its KVM version, on three VMs with SELinux
// enforcing:
//
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario install
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario install-kvm
//
// install-kvm needs /dev/kvm on the machine.
const (
	installScenario    = "install"
	installKVMScenario = "install-kvm"
)

// testInstall starts the lab with empty nodes, VMs with vms, fills the
// registry and runs install.yml: k3s, then every chart. It checks that the
// cluster is healthy, on VMs also that SELinux is enforcing and k3s runs
// in its domain, then that upgrade.yml on the running cluster changes
// nothing. It writes what it saw to results/<scenario>/report.txt.
func testInstall(ctx context.Context, t *testing.T, m *versions.Manifest, vms bool) {
	scenario := installScenario
	if vms {
		scenario = installKVMScenario
	} else {
		node, err := images.Get(ctx, "node")
		if err != nil {
			t.Fatal(err)
		}
		if err := prepareHost(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	l, err := startLab(ctx, t, m, nil, vms)
	if err != nil {
		t.Fatal(err)
	}
	report, env, err := l.deploy(ctx, m, "install.yml")
	if err != nil {
		t.Fatal(err)
	}
	if vms {
		enforcing, err := l.enforcing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		report += "\n" + enforcing
	}
	logf("running upgrade.yml, which changes nothing on the running cluster")
	start := time.Now()
	out, err := l.playbook(ctx, env, "upgrade.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := nodesUnchanged(out); err != nil {
		t.Fatal(err)
	}
	report += fmt.Sprintf("\nupgrade.yml changed nothing, in %s:\n%s\n", time.Since(start).Round(time.Second), recap(out))
	writeReport(t, scenario, report)
}

// selinuxState prints the SELinux mode of a node and the SELinux label of
// its k3s, each on a line, then the SELinux denials in its audit log.
const selinuxState = `getenforce
ps -o label= -p "$(systemctl show --property MainPID --value k3s)"
grep 'avc: *denied' /var/log/audit/audit.log || true`

// enforcing checks that SELinux is enforcing on every node and that k3s
// runs in container_runtime_t, the domain the k3s-selinux policy gives it,
// and returns what it saw, with the denials of each node.
func (l *lab) enforcing(ctx context.Context) (string, error) {
	report := "SELinux on the nodes:\n"
	for i := range nodeCount {
		code, out, err := l.onNode(ctx, i, "sh", "-c", selinuxState)
		if err != nil {
			return "", err
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if code != 0 || len(lines) < 2 || lines[0] != "Enforcing" || !strings.Contains(lines[1], ":container_runtime_t:") {
			return "", fmt.Errorf("SELinux on %s: exit code %d: want Enforcing and k3s in container_runtime_t:\n%s", nodeName(i), code, out)
		}
		denials := lines[2:]
		report += fmt.Sprintf("%s: %s, k3s runs as %s, %d denials in the audit log\n", nodeName(i), lines[0], lines[1], len(denials))
		for _, d := range denials {
			report += "  " + d + "\n"
		}
	}
	return report, nil
}

// deploy fills the registry with every image of the versions file, then
// runs playbook, install.yml or upgrade.yml, with the charts' secrets, and
// waits until the cluster is healthy. It returns what it saw, and the
// secrets, for another run of a playbook. When the playbook or the cluster
// fails, it prints the cluster's state first.
func (l *lab) deploy(ctx context.Context, m *versions.Manifest, playbook string) (string, map[string]string, error) {
	start := time.Now()
	logf("filling the registry with every image of the versions file")
	if err := registry.Fill(ctx, registry.Client(registryAddr), m, registryAddr); err != nil {
		return "", nil, err
	}
	report := fmt.Sprintf("The fill copied %d images in %s.\n", len(m.Pinned()), time.Since(start).Round(time.Second))
	env, err := l.chartSecrets(ctx)
	if err != nil {
		return "", nil, err
	}
	logf("running %s", playbook)
	start = time.Now()
	failed := func(err error) (string, map[string]string, error) {
		logf("%s", l.state(ctx))
		return "", nil, err
	}
	out, err := l.playbook(ctx, env, playbook)
	if err != nil {
		return failed(err)
	}
	report += fmt.Sprintf("\n%s took %s:\n%s\n", playbook, time.Since(start).Round(time.Second), recap(out))
	start = time.Now()
	logf("waiting up to 15 min for the cluster to be healthy")
	err = poll(ctx, time.Now().Add(15*time.Minute), l.clusterHealthy)
	if err != nil {
		return failed(fmt.Errorf("the cluster is not healthy: %w", err))
	}
	logf("waiting up to 10 min for the first backup and for the metrics of every source")
	mon := &monitoring{http: &http.Client{Timeout: 20 * time.Second}}
	err = poll(ctx, time.Now().Add(10*time.Minute), func(ctx context.Context) error {
		return errors.Join(l.backedUp(ctx), mon.everySource(ctx))
	})
	if err != nil {
		return failed(fmt.Errorf("the backup or the monitoring: %w", err))
	}
	report += fmt.Sprintf("\nThe cluster was healthy, with its first backup and the metrics of every source, %s after %s.\n",
		time.Since(start).Round(time.Second), playbook)
	return report, env, nil
}

// chartSecrets are the company CA and the charts' secrets, as the
// environment of ansible-playbook that the role helmfile reads: the store's
// keys of the registry and of the backups, and a new EAB key of the company
// CA for each ACME account.
func (l *lab) chartSecrets(ctx context.Context) (map[string]string, error) {
	env := map[string]string{
		"COMPANY_CA":             string(l.root.certPEM),
		"REGISTRY_HTTP_SECRET":   registryHTTPSecret,
		"REGISTRY_S3_ACCESS_KEY": registryAccess,
		"REGISTRY_S3_SECRET_KEY": registrySecret,
		"BACKUP_S3_ACCESS_KEY":   s3Access,
		"BACKUP_S3_SECRET_KEY":   s3Secret,
	}
	for _, account := range []string{"PG_SERVER", "PG_REPLICATION", "OTEL_CLIENT"} {
		key, err := l.bao.newEAB(ctx)
		if err != nil {
			return nil, err
		}
		env[account+"_EAB_KID"], env[account+"_EAB_HMAC"] = key.id, key.key
	}
	return env, nil
}

// kubectl runs k3s's kubectl on the first node and returns its output.
func (l *lab) kubectl(ctx context.Context, args ...string) (string, error) {
	code, out, err := l.onNode(ctx, 0, append([]string{"k3s", "kubectl"}, args...)...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("kubectl %s: exit code %d: %s", strings.Join(args, " "), code, out)
	}
	return strings.TrimSpace(out), nil
}

// clusterHealthy: the CNPG cluster has three ready instances, the phase
// healthy, and two replicas streaming from the primary.
func (l *lab) clusterHealthy(ctx context.Context) error {
	status, err := l.kubectl(ctx, "get", "cluster", clusterName, "-n", namespace, "-o",
		`jsonpath={.status.readyInstances}/{.status.phase}/{.status.currentPrimary}`)
	if err != nil {
		return err
	}
	ready, phase, primary := "", "", ""
	if parts := strings.SplitN(status, "/", 3); len(parts) == 3 {
		ready, phase, primary = parts[0], parts[1], parts[2]
	}
	if ready != "3" || phase != "Cluster in healthy state" {
		return fmt.Errorf("the cluster %s: ready instances %q, phase %q", clusterName, ready, phase)
	}
	streaming, err := l.kubectl(ctx, "exec", "-n", namespace, primary, "-c", "postgres", "--",
		"psql", "-U", "postgres", "-XqAt", "-c", "select count(*) from pg_stat_replication where state = 'streaming'")
	if err != nil {
		return err
	}
	if streaming != "2" {
		return fmt.Errorf("replicas streaming from %s: %s", primary, streaming)
	}
	return nil
}

// backedUp: a backup of the cluster completed, such as the one its
// ScheduledBackup takes when the cluster is new.
func (l *lab) backedUp(ctx context.Context) error {
	phases, err := l.kubectl(ctx, "get", "backups", "-n", namespace, "-o", "jsonpath={.items[*].status.phase}")
	if err != nil {
		return err
	}
	for _, phase := range strings.Fields(phases) {
		if phase == "completed" {
			return nil
		}
	}
	return fmt.Errorf("no completed backup of %s: %q", clusterName, phases)
}

// labState shows the cluster's state: kubectl's arguments.
var labState = [][]string{
	{"get", "nodes", "-o", "wide"},
	{"get", "pods", "-A", "-o", "wide"},
	{"get", "clusters,backups,certificates,certificaterequests,orders,challenges,issuers", "-A"},
	{"describe", "cluster", clusterName, "-n", namespace},
	{"get", "events", "-A", "--sort-by=.lastTimestamp"},
	{"logs", "-n", "cnpg-system", "-l", "app.kubernetes.io/name=cloudnative-pg", "--tail=50", "--prefix"},
	{"logs", "-n", "registry", "-l", "app.kubernetes.io/name=registry", "--tail=30", "--prefix"},
	{"logs", "-n", namespace, "-l", "cnpg.io/cluster=" + clusterName, "--all-containers", "--tail=30", "--prefix", "--max-log-requests=20"},
}

// state is the cluster's state, for a failed deploy, with the failed pulls
// that containerd's log on each node shows.
func (l *lab) state(ctx context.Context) string {
	state := "The state of the cluster:\n"
	for _, args := range labState {
		out, err := l.kubectl(ctx, args...)
		if err != nil {
			out = err.Error()
		}
		state += fmt.Sprintf("\n$ kubectl %s\n%s\n", strings.Join(args, " "), out)
	}
	for i := range nodeCount {
		_, out, err := l.onNode(ctx, i, "sh", "-c", "grep -iE 'error|fail' "+containerdDir+"/containerd.log | tail -n 20")
		if err != nil {
			out = err.Error()
		}
		state += fmt.Sprintf("\n$ the errors of containerd on %s\n%s\n", nodeName(i), out)
	}
	return state
}

// writeReport logs the report of a scenario and writes it to
// results/<scenario>/report.txt.
func writeReport(t *testing.T, scenario, report string) {
	t.Logf("%s", report)
	dir := "../results/" + scenario
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/report.txt", []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
}
