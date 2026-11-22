package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/images"
	"github.com/djosh34/cnpg-e2e/e2e/registry"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
	"github.com/regclient/regclient/types/ref"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.yaml.in/yaml/v3"
)

// The cache is the lab after install.yml without its helmfile play: a
// clean three-node k3s with no charts and no app images, next to the store
// and the company CA. It is one image per container: each node, the store
// and the CA, each committed after a clean stop, as machines that shut
// down. The packages of this repo hold them, tagged by a hash of
// everything that makes them (cacheTag). Only CI pushes them; a run that
// finds no cache builds it here.
//
// The scenario cache makes sure the packages have the cache of this
// checkout, building it when they do not, and with -push pushing it:
//
//	go -C e2e test -run '^TestScenario$' -count=1 -timeout 60m -v . -scenario cache -push
//
// The scenario restore starts the lab from the cache, pulled or built here,
// and checks that the cluster is Ready, that upgrade.yml changes nothing on
// a restored node, and that the CA issues a certificate.
const (
	cacheScenario   = "cache"
	restoreScenario = "restore"
	// The label of the CA's cache image that holds restoreData.
	restoreLabel = "cnpg-e2e.restore"
)

var pushFlag = flag.Bool("push", false, "with -scenario cache: push a cache it builds to this repo's packages, which only CI does")

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
// what the cache holds: the playbooks and the Go code that starts the lab
// and sets up the store and the CA.
var cacheInputs = []string{"../ansible", "lab_test.go", "cache_test.go", "outside.go", "store.go", "ca.go"}

// cacheTag is the tag of the cache of this checkout: a hash of the images of
// the nodes, the store and the CA, of the versions file's k3s and nodeTools,
// and of the files of cacheInputs.
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
			if d.IsDir() && path == "../ansible/downloads" {
				return fs.SkipDir
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
		if _, err := rc.ManifestHead(ctx, r); err != nil {
			logf("the packages have no %s: %v", r.CommonName(), err)
			missing = true
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

// buildCache starts the lab with empty nodes, runs install.yml without
// its helmfile play, waits until every node is Ready, runs it again to
// check that it changes nothing, and saves the lab as the cache. Its
// containers are gone when it returns.
func buildCache(ctx context.Context, t *testing.T, m *versions.Manifest, tag string) {
	built := t.Run("build the cache", func(t *testing.T) {
		l, err := startLab(ctx, t, m, nil)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		logf("running install.yml")
		out, err := l.playbook(ctx, "install.yml", "--skip-tags", "helmfile")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s", recap(out))
		if err := l.ready(ctx, start); err != nil {
			t.Fatal(err)
		}
		logf("running install.yml again, which changes nothing on the nodes")
		out, err = l.playbook(ctx, "install.yml", "--skip-tags", "helmfile")
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
// the CA, and commits each as its cache image. The CA's image gets
// restoreData as a label.
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
	for _, name := range cachedNames() {
		args := []string{"commit"}
		if name == "acme" {
			args = append(args, "--change", "LABEL "+restoreLabel+"="+base64.StdEncoding.EncodeToString(data))
		}
		if _, err := command(ctx, "docker", append(args, name, cacheRef(name, tag))...); err != nil {
			return err
		}
	}
	return nil
}

// testRestore starts the lab from the cache of this checkout, pulled or
// built here, and checks that every node turns Ready, that upgrade.yml
// without its helmfile play changes nothing on the nodes, and that the
// company CA issues a certificate. It writes what it saw to
// results/restore/report.txt.
func testRestore(ctx context.Context, t *testing.T, m *versions.Manifest) {
	tag, err := cacheTag()
	if err != nil {
		t.Fatal(err)
	}
	from, err := getCache(ctx, t, m, tag)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	l, err := startLab(ctx, t, m, from)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ready(ctx, start); err != nil {
		t.Fatal(err)
	}
	logf("the cluster of the cache %s is Ready %s after the start", tag, time.Since(start).Round(time.Second))
	report := fmt.Sprintf("The cache %s: every node Ready %s after the start.\n", tag, time.Since(start).Round(time.Second))

	logf("running upgrade.yml, which changes nothing on the restored nodes")
	out, err := l.playbook(ctx, "upgrade.yml", "--skip-tags", "helmfile")
	if err != nil {
		t.Fatal(err)
	}
	if err := nodesUnchanged(out); err != nil {
		t.Fatal(err)
	}
	report += "\nupgrade.yml:\n" + recap(out) + "\n"

	logf("getting a certificate from the company CA")
	cert, err := l.issue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report += fmt.Sprintf("\nThe company CA issued a certificate for %s, from %s, valid until %s.\n",
		strings.Join(cert.DNSNames, ", "), cert.Issuer.CommonName, cert.NotAfter.UTC().Format(time.RFC3339))
	t.Logf("%s", report)
	if err := os.MkdirAll("../results/restore", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../results/restore/report.txt", []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
}

// getCache returns the cache of tag: in Docker already, pulled from the
// packages, or built here when they do not have it.
func getCache(ctx context.Context, t *testing.T, m *versions.Manifest, tag string) (*cache, error) {
	c := &cache{images: map[string]string{}}
	for _, name := range cachedNames() {
		c.images[name] = cacheRef(name, tag)
	}
	var missing error
	for _, image := range c.images {
		if _, err := command(ctx, "docker", "image", "inspect", image); err == nil {
			continue
		}
		if _, err := command(ctx, "docker", "pull", "--quiet", image); err != nil {
			missing = err
			break
		}
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
		code, out, err := execIn(ctx, l.nodes[0], []string{"k3s", "kubectl", "get", "nodes", "-o", query})
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("kubectl get nodes: exit code %d: %s", code, out)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
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

// issue gets a certificate for the client's name from the company CA as
// the charts' issuers will: over ACME with a new EAB key, answering the
// http-01 challenge from a web server at the client's address. It checks
// that the certificate chains to the CA's root.
func (l *lab) issue(ctx context.Context) (*x509.Certificate, error) {
	image, err := images.Get(ctx, "controller")
	if err != nil {
		return nil, err
	}
	web, err := l.run(ctx, labContainer{
		name: "client", ip: clientIP, image: image,
		opts: []testcontainers.ContainerCustomizer{
			testcontainers.WithCmd("sh", "-c", "mkdir -p /srv"+http01.ChallengePath("")+" && exec python3 -u -m http.server 80 --directory /srv"),
			testcontainers.WithWaitStrategy(wait.ForLog("Serving HTTP")),
		},
	})
	if err != nil {
		return nil, err
	}
	eab, err := l.bao.newEAB(ctx)
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(l.root.certPEM)
	// This machine has no DNS for the lab's names.
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	account := &acmeAccount{key: key}
	config := lego.NewConfig(account)
	config.CADirURL = acmeDirectory
	config.Certificate.KeyType = certcrypto.EC256
	config.HTTPClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr == acmeName+":443" {
					addr = acmeIP + ":443"
				}
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
	client, err := lego.NewClient(config)
	if err != nil {
		return nil, err
	}
	if err := client.Challenge.SetHTTP01Provider(challengeFiles{ctx: ctx, web: web}); err != nil {
		return nil, err
	}
	account.registration, err = client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
		TermsOfServiceAgreed: true, Kid: eab.id, HmacEncoded: eab.key,
	})
	if err != nil {
		return nil, fmt.Errorf("registering the ACME account: %w", err)
	}
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: []string{clientName}})
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(res.Certificate)
	if block == nil {
		return nil, errors.New("the certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: clientName}); err != nil {
		return nil, err
	}
	return cert, nil
}

// challengeFiles answers http-01 challenges with files in the web server's
// directory.
type challengeFiles struct {
	ctx context.Context
	web *testcontainers.DockerContainer
}

func (c challengeFiles) Present(_, token, keyAuth string) error {
	return c.web.CopyToContainer(c.ctx, []byte(keyAuth), "/srv"+http01.ChallengePath(token), 0o644)
}

func (c challengeFiles) CleanUp(_, _, _ string) error { return nil }

// acmeAccount is the ACME account of issue.
type acmeAccount struct {
	key          crypto.PrivateKey
	registration *registration.Resource
}

func (a *acmeAccount) GetEmail() string                        { return "" }
func (a *acmeAccount) GetRegistration() *registration.Resource { return a.registration }
func (a *acmeAccount) GetPrivateKey() crypto.PrivateKey        { return a.key }
