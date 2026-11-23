package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"go.yaml.in/yaml/v3"
)

// The upgrade scenarios. The image upgrade is the scenario @image-upgrade of
// the feature files: upgrade.yml first deploys every chart with an earlier
// versions file (upgradeFrom), then the scenario applies the checkout's with
// upgrade.yml. The k3s upgrade installs k3s.previous of the versions file on
// empty nodes, then its k3s with upgrade.yml, and its KVM version does the
// same on three VMs with SELinux enforcing:
//
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario image-upgrade
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario k3s-upgrade
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario k3s-upgrade-kvm
//
// k3s-upgrade-kvm needs /dev/kvm on the machine.
const (
	imageUpgradeScenario  = "image-upgrade"
	k3sUpgradeScenario    = "k3s-upgrade"
	k3sUpgradeKVMScenario = "k3s-upgrade-kvm"
)

// The releases of this repo. The nightly puts its versions file into its
// release as the asset versions.yaml.
const (
	releasesURL   = "https://api.github.com/repos/djosh34/cnpg-e2e/releases?per_page=100"
	versionsAsset = "versions.yaml"
)

// testK3sUpgrade starts the lab with empty nodes, VMs with vms, fills the
// registry and installs k3s.previous of the versions file with install.yml,
// with every chart. Then upgrade.yml brings the nodes to the versions file's
// k3s. It checks that every node runs that k3s, that every running container
// is on an image of the versions file and that the cluster is healthy. It
// writes what it saw to results/<scenario>/report.txt.
func testK3sUpgrade(ctx context.Context, t *testing.T, m *versions.Manifest, vms bool) {
	scenario := k3sUpgradeScenario
	if vms {
		scenario = k3sUpgradeKVMScenario
	}
	l := startEmpty(ctx, t, m, vms)
	report, env, err := l.deploy(ctx, m, nil, "install.yml", "--extra-vars", "k3s_previous=true")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.k3sOn(ctx, m.K3s.Previous.Version); err != nil {
		t.Fatal(err)
	}
	report += fmt.Sprintf("\nEvery node ran k3s %s.\n", m.K3s.Previous.Version)
	start := time.Now()
	upgraded, _, err := l.deploy(ctx, m, env, "upgrade.yml")
	if err != nil {
		t.Fatal(err)
	}
	report += "\n" + upgraded
	if err := l.ready(ctx, start); err != nil {
		t.Fatal(err)
	}
	logf("waiting up to 10 min for every running container to be on an image of the versions file")
	err = poll(ctx, time.Now().Add(10*time.Minute), func(ctx context.Context) error {
		pods, err := l.kubectl(ctx, "get", "pods", "--all-namespaces", "--output", "json")
		if err != nil {
			return err
		}
		return errors.Join(l.k3sOn(ctx, m.K3s.Version), containersOn(pods, m))
	})
	if err != nil {
		t.Fatal(err)
	}
	report += fmt.Sprintf("\nEvery node ran k3s %s, and every running container an image of the versions file, %s after the start of upgrade.yml.\n",
		m.K3s.Version, time.Since(start).Round(time.Second))
	if vms {
		enforcing, err := l.enforcing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		report += "\n" + enforcing
	}
	writeReport(t, scenario, report)
}

// k3sOn checks that every node runs k3s version, as its kubelet reports.
func (l *lab) k3sOn(ctx context.Context, version string) error {
	out, err := l.kubectl(ctx, "get", "nodes", "--output",
		`jsonpath={range .items[*]}{.metadata.name} {.status.nodeInfo.kubeletVersion}{"\n"}{end}`)
	if err != nil {
		return err
	}
	lines := strings.Split(out, "\n")
	if len(lines) != nodeCount {
		return fmt.Errorf("%d nodes, not %d: %s", len(lines), nodeCount, out)
	}
	for _, line := range lines {
		if f := strings.Fields(line); len(f) != 2 || f[1] != version {
			return fmt.Errorf("a node does not run k3s %s: %q", version, line)
		}
	}
	return nil
}

// containersOn checks that every running container of pods, the JSON of
// kubectl get pods, runs an image of m: one it pins, by the repository and
// digest that the container's image ID names, or one of its airgap images,
// by the image's tag. An error names each other container.
func containersOn(pods string, m *versions.Manifest) error {
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					Name    string `json:"name"`
					Image   string `json:"image"`
					ImageID string `json:"imageID"`
					State   struct {
						Running *struct{} `json:"running"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(pods), &list); err != nil {
		return fmt.Errorf("the pods: %w", err)
	}
	ms := []*versions.Manifest{m}
	pinned, airgap := pinnedImages(ms), airgapImages(ms)
	var errs []error
	for _, pod := range list.Items {
		for _, c := range pod.Status.ContainerStatuses {
			if c.State.Running == nil {
				continue
			}
			name, digest, _ := strings.Cut(c.ImageID, "@")
			if !slices.Contains(pinned, versions.Repository(name)+"@"+digest) && !slices.Contains(airgap, versions.Reference(c.Image)) {
				errs = append(errs, fmt.Errorf("the container %s of %s/%s runs %s (%s), not an image of the versions file",
					c.Name, pod.Metadata.Namespace, pod.Metadata.Name, c.Image, c.ImageID))
			}
		}
	}
	return errors.Join(errs...)
}

// upgradeFrom is the versions file the image upgrade starts from: the
// versions.yaml of the newest release that has one whose images differ from
// those of m, else the newest version of versions/versions.yaml in the
// checkout's history that does, with the k3s of the checkout's versions
// file: the scenario upgrades images, and k3s takes no downgrade. It
// returns where it is from, its content and what it pins. Before the first
// release, on a pull request that pins new images, that is the versions file
// of main.
func upgradeFrom(ctx context.Context, m *versions.Manifest) (string, []byte, *versions.Manifest, error) {
	releases, err := releaseVersions(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	history, err := historyVersions(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	from, data, err := firstOther(ctx, m, append(releases, history...))
	if err != nil {
		return "", nil, nil, err
	}
	current, err := os.ReadFile(versionsFile)
	if err != nil {
		return "", nil, nil, err
	}
	if data, err = withK3s(data, current); err != nil {
		return "", nil, nil, fmt.Errorf("the versions file of %s: %w", from, err)
	}
	before, err := versions.Parse(data)
	return from, data, before, err
}

// withK3s is the versions file data with the k3s of the versions file
// current.
func withK3s(data, current []byte) ([]byte, error) {
	var file, k3s map[string]any
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(current, &k3s); err != nil {
		return nil, err
	}
	file["k3s"] = k3s["k3s"]
	return yaml.Marshal(file)
}

// versionsSource is a versions file to start the image upgrade from: where
// it is from, and how to read it.
type versionsSource struct {
	name string
	read func(context.Context) ([]byte, error)
}

// firstOther is the first versions file of sources whose images differ from
// those of m.
func firstOther(ctx context.Context, m *versions.Manifest, sources []versionsSource) (string, []byte, error) {
	for _, s := range sources {
		data, err := s.read(ctx)
		if err != nil {
			return "", nil, fmt.Errorf("the versions file of %s: %w", s.name, err)
		}
		before, err := versions.Parse(data)
		if err != nil {
			return "", nil, fmt.Errorf("the versions file of %s: %w", s.name, err)
		}
		if !slices.Equal(before.Pinned(), m.Pinned()) {
			return s.name, data, nil
		}
	}
	return "", nil, errors.New("no release and no earlier versions/versions.yaml of the checkout pins other images; " +
		"a shallow clone has no history: git fetch --unshallow")
}

// releaseVersions are the versions files of this repo's releases, newest
// first: the asset versions.yaml of each release that has one. The
// releases are public. GITHUB_TOKEN, when set, raises the API's rate limit.
func releaseVersions(ctx context.Context) ([]versionsSource, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var releases []struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := callJSON(&http.Client{Timeout: time.Minute}, req, &releases); err != nil {
		return nil, err
	}
	var sources []versionsSource
	for _, r := range releases {
		for _, a := range r.Assets {
			if a.Name == versionsAsset {
				sources = append(sources, versionsSource{name: "the release " + r.TagName, read: func(ctx context.Context) ([]byte, error) {
					return fetch(ctx, a.URL)
				}})
			}
		}
	}
	return sources, nil
}

// historyVersions are the versions of versions/versions.yaml in the
// checkout's history, newest first.
func historyVersions(ctx context.Context) ([]versionsSource, error) {
	out, err := command(ctx, "git", "-C", "..", "log", "--format=%H", "--", "versions/versions.yaml")
	if err != nil {
		return nil, err
	}
	var sources []versionsSource
	for _, commit := range strings.Fields(out) {
		sources = append(sources, versionsSource{name: "the commit " + commit, read: func(ctx context.Context) ([]byte, error) {
			data, err := command(ctx, "git", "-C", "..", "show", commit+":versions/versions.yaml")
			return []byte(data + "\n"), err
		}})
	}
	return sources, nil
}

// upgradeImages: upgrade.yml applies the checkout's versions file, after
// the scenario's start deployed the one it starts from. It keeps the
// releases before and after, for releasesLeftAlone.
func upgradeImages(ctx context.Context, s *scenario, _ args) error {
	data, err := os.ReadFile(versionsFile)
	if err != nil {
		return err
	}
	l := s.w.lab
	if s.releasesBefore, err = l.releases(ctx); err != nil {
		return err
	}
	if err := l.useVersions(ctx, data); err != nil {
		return err
	}
	// CNPG may restart every instance on a new image, the primary last.
	s.primaryOut = true
	report, err := l.apply(ctx, s.w.m, s.w.env, "upgrade.yml")
	if err != nil {
		return err
	}
	if err := s.results.write("upgrade.txt", report); err != nil {
		return err
	}
	s.releasesAfter, err = l.releases(ctx)
	return err
}

// release is a Helm release: its revision, and a hash of its manifest.
type release struct {
	revision, manifest string
}

// releases are the Helm releases of the cluster, by namespace/name, read
// with helm on the first node.
func (l *lab) releases(ctx context.Context) (map[string]release, error) {
	out, err := l.helm(ctx, "list", "--all-namespaces", "--output", "json")
	if err != nil {
		return nil, err
	}
	var list []struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Revision  string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("helm list: %w", err)
	}
	releases := map[string]release{}
	for _, r := range list {
		manifest, err := l.helm(ctx, "get", "manifest", r.Name, "--namespace", r.Namespace)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(manifest))
		releases[r.Namespace+"/"+r.Name] = release{revision: r.Revision, manifest: hex.EncodeToString(sum[:])}
	}
	return releases, nil
}

// helm runs helm as root on the first node, with k3s's admin kubeconfig,
// and returns its output.
func (l *lab) helm(ctx context.Context, args ...string) (string, error) {
	code, out, err := l.onNode(ctx, 0, append([]string{"env", "KUBECONFIG=/etc/rancher/k3s/k3s.yaml", "helm"}, args...)...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("helm %s: exit code %d: %s", strings.Join(args, " "), code, out)
	}
	return out, nil
}

// releasesLeftAlone: each release whose manifest the upgrade did not change
// kept its revision, since helmfile upgrades only releases that changed,
// and at least one release changed.
func releasesLeftAlone(_ context.Context, s *scenario, _ args) error {
	var changed []string
	var errs []error
	for name, after := range s.releasesAfter {
		before, ok := s.releasesBefore[name]
		switch {
		case !ok || before.manifest != after.manifest:
			changed = append(changed, name)
		case before.revision != after.revision:
			errs = append(errs, fmt.Errorf("the release %s went from revision %s to %s with the same manifest", name, before.revision, after.revision))
		}
	}
	for name := range s.releasesBefore {
		if _, ok := s.releasesAfter[name]; !ok {
			errs = append(errs, fmt.Errorf("the release %s is gone", name))
		}
	}
	if len(changed) == 0 {
		errs = append(errs, errors.New("the upgrade changed no release"))
	}
	slices.Sort(changed)
	s.detail = "changed: " + strings.Join(changed, ", ")
	return errors.Join(errs...)
}

// onVersionsFile: every running container of the cluster runs an image of
// the checkout's versions file.
func onVersionsFile(ctx context.Context, s *scenario, _ args) error {
	pods, err := kubectl(ctx, "get", "pods", "--all-namespaces", "--output", "json")
	if err != nil {
		return err
	}
	return containersOn(pods, s.w.m)
}
