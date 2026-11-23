package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/fill"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"go.yaml.in/yaml/v3"
)

// The words of the vocabulary that set up a scenario's machines and
// install on them.

// emptyMachines: "Given empty nodes" or "Given empty VMs".
func (s *scenario) emptyMachines(vms bool) error {
	s.cache = "not used: starts from empty nodes"
	if vms {
		s.cache = "not used: starts from empty VMs"
	}
	var err error
	s.lab, err = startLab(s.ctx, s.m, vms, nil)
	return err
}

// runningCluster: "Given a running cluster": the cache of the merge base
// with main, or when it is missing or does not come up, empty nodes; then
// install.yml, and a cluster that is healthy, has its backup of the most
// recent schedule and the metrics of every source.
func (s *scenario) runningCluster() error {
	ctx := s.ctx
	start := time.Now()
	commit, err := mergeBase(ctx)
	var c *cached
	if err == nil {
		c, err = pull(ctx, commit)
	}
	if err == nil {
		logf("starting from the cache of %s", commit)
		if s.lab, err = startLab(ctx, s.m, false, c); err == nil {
			err = s.lab.writeKubeconfig(ctx)
			if err == nil {
				err = ready(ctx, start)
			}
			if err != nil {
				err = errors.Join(fmt.Errorf("the restored cluster: %w", err), s.lab.close(ctx))
			}
		}
	}
	if err != nil {
		s.cache = "not used: " + oneLine(err.Error())
		logf("the cache is %s", s.cache)
		if s.lab, err = startLab(ctx, s.m, false, nil); err != nil {
			return err
		}
	} else {
		s.cache = "used " + commit
	}
	if err := s.install(ctx, s.data); err != nil {
		return err
	}
	logf("waiting for a healthy cluster, its backup and the metrics of every source")
	err = poll(ctx, time.Now().Add(15*time.Minute), func(ctx context.Context) error { return healthy(ctx, clusterName) })
	if err != nil {
		return err
	}
	err = poll(ctx, time.Now().Add(10*time.Minute), func(ctx context.Context) error {
		return errors.Join(backedUp(ctx), s.lab.mon.everySource(ctx))
	})
	if err != nil {
		return err
	}
	if c != nil && strings.HasPrefix(s.cache, "used") {
		primary, err := clusterStatus(ctx, clusterName, "currentPrimary")
		if err == nil {
			var row string
			row, err = psql(ctx, primary, "postgres", "select commit || ' ' || saved from e2e_cache")
			s.cache += "; the row written before the save: " + row
		}
		if err != nil {
			s.cache += "; the row written before the save: " + oneLine(err.Error())
		}
	}
	s.started = time.Now()
	s.detail = s.cache
	// The roles of the instances as the scenario starts.
	_, err = s.role("the primary")
	return err
}

// install fills the registry and files/ from the versions file data, then
// runs install.yml.
func (s *scenario) install(ctx context.Context, data []byte) error {
	logf("fill")
	if err := fill.Run(ctx, data, registryAddr, true); err != nil {
		return err
	}
	logf("install.yml")
	out, err := s.lab.playbook(ctx, s.lab.env, "install.yml")
	if err != nil {
		return err
	}
	s.detail = oneLine(recap(out))
	if err := s.lab.writeKubeconfig(ctx); err != nil {
		return err
	}
	if !s.up {
		s.up, s.started = true, time.Now()
	}
	return nil
}

// installed: "the versions file is installed".
func installed(ctx context.Context, s *scenario, _ args) error {
	err := s.install(ctx, s.data)
	if s.before != nil && slices.Equal(s.before.Pinned, s.m.Pinned) {
		s.detail = "no image pin changed; " + s.detail
	}
	return err
}

// mainInstalled: "main's versions file is installed", with the k3s release
// before the run's, as A of the upgrade.
func mainInstalled(ctx context.Context, s *scenario, _ args) error {
	main, err := command(ctx, "git", "show", "origin/main:"+versions.File)
	if err != nil {
		return err
	}
	var a, b map[string]any
	if err := errors.Join(yaml.Unmarshal([]byte(main), &a), yaml.Unmarshal(s.data, &b)); err != nil {
		return err
	}
	k3s, ok := b["k3s"].(map[string]any)
	previous, ok2 := k3s["previous"].(map[string]any)
	if !ok || !ok2 || a == nil {
		return fmt.Errorf("no k3s.previous in %s, or no main's", versions.File)
	}
	for k, v := range previous {
		k3s[k] = v
	}
	delete(k3s, "previous")
	a["k3s"] = k3s
	data, err := yaml.Marshal(a)
	if err != nil {
		return err
	}
	if s.before, err = versions.Parse(data); err != nil {
		return fmt.Errorf("main's versions file: %w", err)
	}
	return s.install(ctx, data)
}

// A host's line of the play recap of ansible-playbook.
var recapLine = regexp.MustCompile(`(?m)^(\S+)\s+:\s+ok=\d+\s+changed=(\d+)\s.*$`)

func recap(out string) string {
	return strings.Join(recapLine.FindAllString(out, -1), "\n")
}

// idempotent: a second run of install.yml changes nothing on any node.
func idempotent(ctx context.Context, s *scenario, _ args) error {
	out, err := s.lab.playbook(ctx, s.lab.env, "install.yml")
	if err != nil {
		return err
	}
	nodes := 0
	for _, m := range recapLine.FindAllStringSubmatch(out, -1) {
		nodes++
		if m[2] != "0" {
			return fmt.Errorf("install.yml changed %s on %s:\n%s", m[2], m[1], out)
		}
	}
	if nodes != nodeCount {
		return fmt.Errorf("the play recap has %d nodes, not %d:\n%s", nodes, nodeCount, out)
	}
	s.detail = oneLine(recap(out))
	return nil
}

// selinuxState prints the SELinux mode of a node, the label of its k3s and
// its denials; grep exits with 1 when there is none.
const selinuxState = `getenforce
ps -o label= -p "$(systemctl show --property MainPID --value k3s)"
grep 'avc: *denied' /var/log/audit/audit.log || [ $? -eq 1 ]`

// enforcing: SELinux enforces on every node, and k3s runs in
// container_runtime_t, the domain of the k3s-selinux policy.
func enforcing(ctx context.Context, s *scenario, _ args) error {
	var report []string
	for i := range nodeCount {
		code, out, err := s.lab.onNode(ctx, i, "sh", "-c", selinuxState)
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if code != 0 || len(lines) < 2 || lines[0] != "Enforcing" || !strings.Contains(lines[1], ":container_runtime_t:") {
			return fmt.Errorf("SELinux on %s: exit code %d: want Enforcing and k3s in container_runtime_t:\n%s", nodeName(i), code, out)
		}
		report = append(report, fmt.Sprintf("%s: %d denials", nodeName(i), len(lines)-2))
	}
	s.detail = strings.Join(report, ", ")
	return nil
}

// onK3s: every node runs the k3s of the versions file, as its kubelet says.
func onK3s(ctx context.Context, s *scenario, _ args) error {
	out, err := kubectl(ctx, "get", "nodes", "--output", `jsonpath={range .items[*]}{.metadata.name} {.status.nodeInfo.kubeletVersion}{"\n"}{end}`)
	if err != nil {
		return err
	}
	n := 0
	for line := range strings.Lines(out) {
		if f := strings.Fields(line); len(f) != 2 || f[1] != s.m.K3s.Version {
			return fmt.Errorf("a node does not run k3s %s: %q", s.m.K3s.Version, line)
		}
		n++
	}
	if n != nodeCount {
		return fmt.Errorf("%d nodes, not %d", n, nodeCount)
	}
	return nil
}

type containerStatus struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	ImageID string `json:"imageID"`
	State   struct {
		Running *struct{} `json:"running"`
	} `json:"state"`
}

// onVersionsFile: every running container runs an image of the versions
// file: one it pins, by the digest of its image ID, or an airgap image of
// k3s, by its tag.
func onVersionsFile(ctx context.Context, s *scenario, _ args) error {
	out, err := kubectl(ctx, "get", "pods", "--all-namespaces", "--output", "json")
	if err != nil {
		return err
	}
	var pods struct {
		Items []struct {
			Metadata struct{ Namespace, Name string } `json:"metadata"`
			Status   struct {
				ContainerStatuses     []containerStatus `json:"containerStatuses"`
				InitContainerStatuses []containerStatus `json:"initContainerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &pods); err != nil {
		return fmt.Errorf("the pods: %w", err)
	}
	var airgap []string
	for _, ref := range s.m.K3s.Images {
		airgap = append(airgap, versions.Reference(ref))
	}
	var errs []error
	for _, pod := range pods.Items {
		// A sidecar, such as barman-cloud's, is an init container that runs.
		for _, c := range append(pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses...) {
			name, digest, _ := strings.Cut(c.ImageID, "@")
			if c.State.Running != nil && !slices.Contains(s.m.Pinned, versions.Repository(name)+"@"+digest) &&
				!slices.Contains(airgap, versions.Reference(c.Image)) {
				errs = append(errs, fmt.Errorf("%s of %s/%s runs %s (%s)", c.Name, pod.Metadata.Namespace, pod.Metadata.Name, c.Image, c.ImageID))
			}
		}
	}
	return errors.Join(errs...)
}

// backedUp: a backup finished at or after the most recent time of its
// schedule, so no scheduled backup is due in the scenario. A restored
// cluster takes the one it missed.
func backedUp(ctx context.Context) error {
	out, err := kubectl(ctx, "get", "scheduledbackup", clusterName, "-n", namespace, "-o",
		"jsonpath={.status.lastScheduleTime} {.status.nextScheduleTime}")
	if err != nil {
		return err
	}
	l, n, _ := strings.Cut(out, " ")
	last, err := time.Parse(time.RFC3339, l)
	if err != nil {
		return fmt.Errorf("the schedule of %s: %q", clusterName, out)
	}
	next, err := time.Parse(time.RFC3339, n)
	if err != nil {
		return fmt.Errorf("the schedule of %s: %q", clusterName, out)
	}
	due := last
	if !next.After(time.Now()) {
		due = next
	}
	stopped, err := kubectl(ctx, "get", "backups", "-n", namespace, "-o",
		`jsonpath={range .items[?(@.status.phase=="completed")]}{.status.stoppedAt}{"\n"}{end}`)
	if err != nil {
		return err
	}
	for line := range strings.Lines(stopped) {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(line)); err == nil && !t.Before(due) {
			return nil
		}
	}
	return fmt.Errorf("no backup of %s finished since %s", clusterName, due.Format(time.RFC3339))
}

// writeKubeconfig writes k3s's admin kubeconfig of the first node, at its
// address, and points kubectl at it.
func (l *lab) writeKubeconfig(ctx context.Context) error {
	code, config, err := l.onNode(ctx, 0, "cat", "/etc/rancher/k3s/k3s.yaml")
	if err == nil && code != 0 {
		err = fmt.Errorf("no kubeconfig on %s: %s", nodeName(0), config)
	}
	if err != nil {
		return err
	}
	file, err := cacheFile("kubeconfig")
	if err != nil {
		return err
	}
	kubeconfig = file
	return writePrivate(file, strings.ReplaceAll(config, "https://127.0.0.1:6443", "https://"+nodeIP(0)+":6443"))
}
