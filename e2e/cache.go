package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/images"
	"github.com/djosh34/cnpg-e2e/e2e/registry"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"github.com/regclient/regclient/types/errs"
	"github.com/regclient/regclient/types/ref"
	"go.yaml.in/yaml/v3"
)

// The cache is the lab after install.yml without its helmfile step: a clean
// three-node k3s with no charts and no app images, next to the store and
// the company CA. It is one image per container: each node, the store and
// the CA, each committed after a clean stop, as machines that shut down.
// The packages of this repo hold them, tagged by a hash of everything that
// makes them (cacheTag). Only CI pushes them; a run that finds no cache
// builds it here.
//
// The scenario cache makes sure the packages have the cache of this
// checkout, building it when they do not, and with -push pushing it:
//
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 50m -v . -scenario cache -push
//
// The scenario restore starts the lab from the cache, pulled or built here,
// checks that the cluster is Ready, fills the registry and runs upgrade.yml,
// which applies every chart, and checks that the cluster is healthy, as
// every scenario of the feature files starts (feature.go). With -no-build
// those fail where they would build the cache, as in CI, where the cache
// job published it before.
const (
	cacheScenario   = "cache"
	restoreScenario = "restore"
	// The label of the CA's cache image that holds restoreData.
	restoreLabel = "cnpg-e2e.restore"
	// The label of every cache image with the ID of the build that made it.
	buildLabel = "cnpg-e2e.build"
)

var (
	pushFlag    = flag.Bool("push", false, "with -scenario cache: push a cache it builds to this repo's packages, which only CI does")
	noBuildFlag = flag.Bool("no-build", false, "with a scenario that starts from the cache: fail when the cache cannot be pulled, instead of building it")
)

// cache is a cache to start the lab from: the image of each container it
// holds, and what the CA's image holds besides its data.
type cache struct {
	images        map[string]string
	root          *ca
	unseal, token string
}

// restoreData is what restoring the CA needs: its root, whose key signs the
// TLS certificates of the store and of the CA itself, OpenBao's unseal key
// and its root token. All of it is the cache's own, made for it alone.
type restoreData struct {
	CACert string `json:"caCert"`
	CAKey  string `json:"caKey"`
	Unseal string `json:"unseal"`
	Token  string `json:"token"`
}

// cachedNames are the containers the cache holds: the store, the CA and
// the nodes. Every other container starts from its image as on a new run.
func cachedNames() []string {
	names := []string{"s3", "acme"}
	for i := range nodeCount {
		names = append(names, nodeName(i))
	}
	return names
}

func cacheRef(name, tag string) string {
	return versions.Packages + "/cache-" + name + ":" + tag
}

// cacheInputs are the files besides images/ and the versions file that make
// what the cache holds: the playbooks but the role helmfile and
// rotate-ca.yml, which the cache leaves out, and the Go code that starts the
// lab, names its machines and addresses, and sets up the store and the CA.
var cacheInputs = []string{
	"../ansible", "lab.go", "cache.go", "outside.go", "store.go", "ca.go", "nodes.go", "client.go",
}

// cacheTag is the tag of the cache of this checkout: a hash of the images of
// the nodes, the store and the CA, of the versions file's k3s, without the
// release before and the CA script, and nodeTools, and of the files of
// cacheInputs.
func cacheTag() (string, error) {
	h := sha256.New()
	for _, name := range []string{"node", "s3", "acme"} {
		image, err := images.Ref(name)
		if err != nil {
			return "", err
		}
		if _, err := fmt.Fprintln(h, image); err != nil {
			return "", err
		}
	}
	data, err := os.ReadFile(versionsFile)
	if err != nil {
		return "", err
	}
	var all map[string]any
	if err := yaml.Unmarshal(data, &all); err != nil {
		return "", err
	}
	if k3s, ok := all["k3s"].(map[string]any); ok {
		delete(k3s, "previous")
		delete(k3s, "rotate-ca")
	}
	for _, key := range []string{"k3s", "nodeTools"} {
		// yaml sorts the keys of a map, so the same values give the same text.
		part, err := yaml.Marshal(all[key])
		if err != nil {
			return "", err
		}
		if _, err := h.Write(part); err != nil {
			return "", err
		}
	}
	for _, input := range cacheInputs {
		err := filepath.WalkDir(input, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// What download.yml writes next to the playbooks.
			if d.IsDir() && (path == "../ansible/downloads" || path == "../ansible/roles/helmfile") {
				return fs.SkipDir
			}
			if path == "../ansible/rotate-ca.yml" {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(h, "%s %d\n", path, len(data)); err != nil {
				return err
			}
			_, err = h.Write(data)
			return err
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:20], nil
}

// testCache makes sure this repo's packages have the cache of this
// checkout. When they do not, it builds the cache, and pushes it with -push.
func testCache(ctx context.Context, t *testing.T, m *versions.Manifest) {
	tag, err := cacheTag()
	if err != nil {
		t.Fatal(err)
	}
	rc := registry.Client()
	missing := false
	for _, name := range cachedNames() {
		r, err := ref.New(cacheRef(name, tag))
		if err != nil {
			t.Fatal(err)
		}
		_, err = rc.ManifestHead(ctx, r)
		switch {
		case errors.Is(err, errs.ErrNotFound):
			logf("the packages have no %s", r.CommonName())
			missing = true
		case err != nil:
			t.Fatal(err)
		}
	}
	if !missing {
		logf("the packages have the cache %s", tag)
		return
	}
	buildCache(ctx, t, m, tag)
	if !*pushFlag {
		logf("built the cache %s; without -push it stays here", tag)
		return
	}
	for _, name := range cachedNames() {
		if _, err := command(ctx, "docker", "push", "--quiet", cacheRef(name, tag)); err != nil {
			t.Fatal(err)
		}
		logf("pushed %s", cacheRef(name, tag))
	}
}

// buildCache starts the lab with empty nodes, runs install.yml without its
// helmfile step, waits until every node is Ready, runs it again to check
// that it changes nothing, and saves the lab as the cache. Its containers
// are gone when it returns.
func buildCache(ctx context.Context, t *testing.T, m *versions.Manifest, tag string) {
	built := t.Run("build the cache", func(t *testing.T) {
		node, err := images.Get(ctx, "node")
		if err != nil {
			t.Fatal(err)
		}
		if err := prepareHost(ctx, node); err != nil {
			t.Fatal(err)
		}
		l, err := startLab(ctx, t, m, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		logf("running install.yml without helmfile")
		out, err := l.playbook(ctx, nil, "install.yml", "--skip-tags", "helmfile")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s", recap(out))
		if err := l.ready(ctx, start); err != nil {
			t.Fatal(err)
		}
		logf("running install.yml again, which changes nothing on the nodes")
		out, err = l.playbook(ctx, nil, "install.yml", "--skip-tags", "helmfile")
		if err != nil {
			t.Fatal(err)
		}
		if err := nodesUnchanged(out); err != nil {
			t.Fatal(err)
		}
		if err := l.save(ctx, tag); err != nil {
			t.Fatal(err)
		}
	})
	if !built {
		t.FailNow()
	}
}

// save stops k3s and its pods on each node, then the nodes, the store and
// the CA, and commits each as its cache image, all with one build ID. The
// CA's image gets restoreData as a label.
func (l *lab) save(ctx context.Context, tag string) error {
	logf("saving the lab as the cache %s", tag)
	for i, node := range l.nodes {
		code, out, err := execIn(ctx, node, []string{"k3s-killall.sh"})
		if err != nil {
			return fmt.Errorf("k3s-killall.sh on %s: %w", nodeName(i), err)
		}
		if code != 0 {
			return fmt.Errorf("k3s-killall.sh on %s: exit code %d\n%s", nodeName(i), code, out)
		}
	}
	timeout := time.Minute
	for _, name := range cachedNames() {
		if err := l.containers[name].Stop(ctx, &timeout); err != nil {
			return fmt.Errorf("stopping %s: %w", name, err)
		}
	}
	data, err := json.Marshal(restoreData{
		CACert: string(l.root.certPEM), CAKey: string(l.root.keyPEM), Unseal: l.bao.unseal, Token: l.bao.token,
	})
	if err != nil {
		return err
	}
	build := make([]byte, 8)
	if _, err := rand.Read(build); err != nil {
		return err
	}
	for _, name := range cachedNames() {
		// docker commit copies the container's labels into the image, and
		// Ryuk removes whatever has the run's session ID once the lab's
		// containers are gone, so the image gets none.
		args := []string{"commit", "--change", "LABEL org.testcontainers.sessionId=",
			"--change", "LABEL " + buildLabel + "=" + hex.EncodeToString(build)}
		if name == "acme" {
			args = append(args, "--change", "LABEL "+restoreLabel+"="+base64.StdEncoding.EncodeToString(data))
		}
		if _, err := command(ctx, "docker", append(args, name, cacheRef(name, tag))...); err != nil {
			return err
		}
	}
	return nil
}

// testRestore starts the lab from the cache, then deploys every chart with
// upgrade.yml and checks that the cluster is healthy. It writes what it saw
// to results/restore/report.txt.
func testRestore(ctx context.Context, t *testing.T, m *versions.Manifest) {
	l, report, _ := startCached(ctx, t, m)
	deployed, _, err := l.deploy(ctx, m, nil, "upgrade.yml")
	if err != nil {
		t.Fatal(err)
	}
	writeReport(t, restoreScenario, report+"\n"+deployed)
}

// startCached starts the lab from the cache of this checkout, pulled or
// built here, and waits until every node is Ready. It returns the lab, what
// it saw, and when the lab started.
func startCached(ctx context.Context, t *testing.T, m *versions.Manifest) (*lab, string, time.Time) {
	tag, err := cacheTag()
	if err != nil {
		t.Fatal(err)
	}
	from, err := getCache(ctx, t, m, tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareHost(ctx, from.images[nodeName(0)]); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	l, err := startLab(ctx, t, m, from, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ready(ctx, start); err != nil {
		t.Fatal(err)
	}
	report := fmt.Sprintf("The cache %s: every node Ready %s after the start.\n", tag, time.Since(start).Round(time.Second))
	logf("%s", strings.TrimSpace(report))
	return l, report, start
}

// getCache returns the cache of tag: in Docker already, pulled from the
// packages, or built here when they do not have it.
func getCache(ctx context.Context, t *testing.T, m *versions.Manifest, tag string) (*cache, error) {
	c := &cache{images: map[string]string{}}
	for _, name := range cachedNames() {
		c.images[name] = cacheRef(name, tag)
	}
	// One cluster's images: a cache built here and one CI pushed have the
	// same tag but different clusters, and an interrupted pull can leave
	// both. The images here count only when all are of one build.
	builds := map[string]bool{}
	for _, image := range c.images {
		build, err := command(ctx, "docker", "image", "inspect", "--format", `{{index .Config.Labels "`+buildLabel+`"}}`, image)
		if err != nil {
			build = ""
		}
		builds[build] = true
	}
	local := len(builds) == 1 && !builds[""]
	var missing error
	if !local {
		for _, image := range c.images {
			if _, missing = command(ctx, "docker", "pull", "--quiet", image); missing != nil {
				break
			}
		}
	}
	if missing != nil && *noBuildFlag {
		return nil, fmt.Errorf("pulling the cache %s: %w", tag, missing)
	}
	if missing != nil {
		logf("building the cache %s here, since pulling it failed: %v", tag, missing)
		buildCache(ctx, t, m, tag)
	}
	label, err := command(ctx, "docker", "image", "inspect", "--format", `{{index .Config.Labels "`+restoreLabel+`"}}`, c.images["acme"])
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(label)
	if err != nil {
		return nil, fmt.Errorf("the label %s of %s: %w", restoreLabel, c.images["acme"], err)
	}
	var r restoreData
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("the label %s of %s: %w", restoreLabel, c.images["acme"], err)
	}
	c.root, err = parseCA([]byte(r.CACert), []byte(r.CAKey))
	c.unseal, c.token = r.Unseal, r.Token
	return c, err
}

// parseCA reads a CA from its PEM certificate and key, as newCA writes them.
func parseCA(certPEM, keyPEM []byte) (*ca, error) {
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, errors.New("the CA's certificate or key is not PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the CA's key is a %T, not ECDSA", parsed)
	}
	return &ca{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}, nil
}

// ready waits until every node is Ready with a heartbeat from after since.
// A restored etcd still holds the Ready of before the save, which does not
// count.
func (l *lab) ready(ctx context.Context, since time.Time) error {
	query := `jsonpath={range .items[*]}{.metadata.name} {.status.conditions[?(@.type=="Ready")].status} ` +
		`{.status.conditions[?(@.type=="Ready")].lastHeartbeatTime}{"\n"}{end}`
	err := poll(ctx, time.Now().Add(5*time.Minute), func(ctx context.Context) error {
		out, err := l.kubectl(ctx, "get", "nodes", "-o", query)
		if err != nil {
			return err
		}
		lines := strings.Split(out, "\n")
		if len(lines) != nodeCount {
			return fmt.Errorf("%d nodes, not %d: %s", len(lines), nodeCount, out)
		}
		for _, line := range lines {
			f := strings.Fields(line)
			if len(f) != 3 || f[1] != "True" {
				return fmt.Errorf("a node is not Ready: %q", line)
			}
			beat, err := time.Parse(time.RFC3339, f[2])
			if err != nil {
				return err
			}
			if beat.Before(since.Truncate(time.Second)) {
				return fmt.Errorf("node %s: its last heartbeat is from before the start", f[0])
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("every node Ready: %w", err)
	}
	return nil
}

// A host's line of the play recap of ansible-playbook.
var recapLine = regexp.MustCompile(`(?m)^(\S+)\s+:\s+ok=\d+\s+changed=(\d+)\s.*$`)

// recap is the play recap of the output of ansible-playbook.
func recap(out string) string {
	return strings.Join(recapLine.FindAllString(out, -1), "\n")
}

// nodesUnchanged checks the play recap of the output of ansible-playbook:
// each node is there, and no task changed anything on it.
func nodesUnchanged(out string) error {
	nodes := 0
	for _, match := range recapLine.FindAllStringSubmatch(out, -1) {
		if match[1] == "localhost" {
			continue
		}
		nodes++
		if match[2] != "0" {
			return fmt.Errorf("the playbook changed %s on %s:\n%s", match[2], match[1], out)
		}
	}
	if nodes != nodeCount {
		return fmt.Errorf("the play recap has %d nodes, not %d:\n%s", nodes, nodeCount, out)
	}
	return nil
}
