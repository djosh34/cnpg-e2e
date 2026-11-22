package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/images"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
	moby "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	mobynet "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
)

// The lab is what the e2e test starts on the machine's own Docker, with only
// Go and Docker: the services outside k3s, three empty nodes and the
// controller, each a container at a fixed address on one network with no
// route to the internet. The machine itself reaches them on that network.
// The images are this repo's packages, or built here where a package does
// not have one yet (images.Get). testcontainers removes every container and
// the network when the test ends, and Ryuk does when the test is killed.
const (
	// The writable registry that the fill copies the images into, on the
	// store's registry bucket, which the registry of every node reads too.
	registryIP     = "10.77.0.21"
	registryBucket = "registry"
	// The machine that runs Ansible.
	controllerIP = "10.77.0.5"
)

var registryAddr = registryIP + ":5000"

// The writable registry's config: the registry bucket over the store's
// TLS, which it trusts through SSL_CERT_FILE, and no redirects, so its
// clients never need the store. Its readers will share http.secret.
const registryConfig = `version: 0.1
storage:
  s3:
    accesskey: ` + s3RootAccess + `
    secretkey: ` + s3RootSecret + `
    region: ` + s3Region + `
    regionendpoint: https://` + s3Name + `
    forcepathstyle: true
    bucket: ` + registryBucket + `
  redirect:
    disable: true
http:
  addr: :5000
  secret: example-registry-secret
`

// The inventory of the controller's Ansible.
func inventory() string {
	lines := []string{"[nodes]"}
	for i := range nodeCount {
		lines = append(lines, nodeHost(i))
	}
	return strings.Join(append(lines, "", "[nodes:vars]", "ansible_user=root"), "\n") + "\n"
}

type lab struct {
	t          *testing.T
	network    *testcontainers.DockerNetwork
	nodes      []*testcontainers.DockerContainer
	controller *testcontainers.DockerContainer
}

// startLab starts the lab: the services outside k3s, set up and answering,
// three nodes with sshd running, and the controller with an SSH key that
// every node takes.
func startLab(ctx context.Context, t *testing.T, m *versions.Manifest) (*lab, error) {
	root, err := newCA()
	if err != nil {
		return nil, err
	}
	n, err := tcnetwork.New(ctx, tcnetwork.WithInternal(), tcnetwork.WithIPAM(&mobynet.IPAM{
		Config: []mobynet.IPAMConfig{{Subnet: netip.MustParsePrefix(subnet), Gateway: netip.MustParseAddr(gatewayIP)}},
	}))
	testcontainers.CleanupNetwork(t, n)
	if err != nil {
		return nil, err
	}
	l := &lab{t: t, network: n}
	own := map[string]string{}
	names, err := images.Names()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if own[name], err = images.Get(ctx, name); err != nil {
			return nil, err
		}
	}

	logf("starting the DNS, the company CA, the store and the registry")
	_, err = l.run(ctx, labContainer{name: "dns", ip: dnsIP, image: own["dns"], files: map[string]string{"/etc/coredns/hosts": hosts()}})
	if err != nil {
		return nil, err
	}
	if err := l.startACME(ctx, root, own["acme"]); err != nil {
		return nil, err
	}
	if err := l.startStore(ctx, root); err != nil {
		return nil, err
	}
	if err := l.startRegistry(ctx, root, m.Registry.Image); err != nil {
		return nil, err
	}
	logf("starting the monitoring")
	if err := l.startMonitoring(ctx, root, own); err != nil {
		return nil, err
	}
	logf("starting the nodes and the controller")
	key, authorized, err := sshKey()
	if err != nil {
		return nil, err
	}
	for i := range nodeCount {
		node, err := l.run(ctx, labContainer{
			name: nodeName(i), ip: nodeIP(i), image: own["node"],
			secrets: map[string]string{"/root/.ssh/authorized_keys": authorized},
			// A node is up once systemd started sshd and the default route.
			opts: []testcontainers.ContainerCustomizer{
				testcontainers.WithHostConfigModifier(func(h *moby.HostConfig) {
					h.Privileged = true
					h.CgroupnsMode = moby.CgroupnsModePrivate
					h.Tmpfs = map[string]string{"/run": "", "/run/lock": "", "/tmp": ""}
				}),
				testcontainers.WithWaitStrategy(wait.ForExec([]string{"systemctl", "is-active", "sshd", "default-route"}).
					WithStartupTimeout(2 * time.Minute)),
			},
		})
		if err != nil {
			return nil, err
		}
		l.nodes = append(l.nodes, node)
	}
	l.controller, err = l.run(ctx, labContainer{
		name: "controller", ip: controllerIP, image: own["controller"],
		files:   map[string]string{"/etc/ansible/hosts": inventory()},
		secrets: map[string]string{"/root/.ssh/id_ed25519": key},
		// The nodes are new on every run.
		opts: []testcontainers.ContainerCustomizer{testcontainers.WithEnv(map[string]string{"ANSIBLE_HOST_KEY_CHECKING": "False"})},
	})
	return l, err
}

// sshKey is a new SSH key of the controller: its private key, and the line
// of the nodes' authorized_keys.
func sshKey() (private, authorized string, _ error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), string(ssh.MarshalAuthorizedKey(sshPub)), nil
}

// startACME starts the company CA and sets it up.
func (l *lab) startACME(ctx context.Context, root *ca, image string) error {
	cert, key, err := root.serverCert(acmeName)
	if err != nil {
		return err
	}
	_, err = l.run(ctx, labContainer{
		name: "acme", ip: acmeIP, image: image,
		files: map[string]string{"/bao/tls.crt": string(cert), "/bao/tls.key": string(key)},
	})
	if err != nil {
		return err
	}
	_, err = setUpACME(ctx, root)
	return err
}

// startStore starts the store, sets it up and makes the registry's bucket.
// Its data is on volumes, not in the container's own layer.
func (l *lab) startStore(ctx context.Context, root *ca) error {
	cert, key, err := root.serverCert(s3Name)
	if err != nil {
		return err
	}
	_, err = l.run(ctx, labContainer{
		name: "s3", ip: s3IP, image: s3Image,
		files: map[string]string{"/tls/tls.crt": string(cert), "/tls/tls.key": string(key)},
		opts: []testcontainers.ContainerCustomizer{
			testcontainers.WithEntrypoint("/usr/local/bin/versitygw"),
			testcontainers.WithCmd(s3Args...),
			testcontainers.WithHostConfigModifier(func(h *moby.HostConfig) {
				for _, dir := range []string{"/data", "/iam", "/versions"} {
					h.Mounts = append(h.Mounts, mount.Mount{Type: mount.TypeVolume, Target: dir})
				}
			}),
		},
	})
	if err != nil {
		return err
	}
	if err := setUpStore(ctx, root); err != nil {
		return err
	}
	return newS3(root, s3RootAccess, s3RootSecret).want(ctx, http.MethodPut, "/"+registryBucket, "", 200)
}

// startRegistry starts the writable registry on the registry bucket, with
// the registry image of the versions file, and waits until it answers.
func (l *lab) startRegistry(ctx context.Context, root *ca, image string) error {
	_, err := l.run(ctx, labContainer{
		name: "registry", ip: registryIP, image: image,
		files: map[string]string{"/etc/distribution/config.yml": registryConfig, "/etc/distribution/ca.crt": string(root.certPEM)},
		opts:  []testcontainers.ContainerCustomizer{testcontainers.WithEnv(map[string]string{"SSL_CERT_FILE": "/etc/distribution/ca.crt"})},
	})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	err = poll(ctx, time.Now().Add(time.Minute), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+registryAddr+"/v2/", nil)
		if err != nil {
			return err
		}
		return callJSON(client, req, nil)
	})
	if err != nil {
		return fmt.Errorf("the registry answering: %w", err)
	}
	return nil
}

// startMonitoring starts the OTel service, Prometheus, Alertmanager and
// Grafana, and waits until each answers.
func (l *lab) startMonitoring(ctx context.Context, root *ca, own map[string]string) error {
	cert, key, err := root.serverCert(otelName)
	if err != nil {
		return err
	}
	containers := []labContainer{
		{
			name: "otel", ip: otelIP, image: own["otel"],
			files: map[string]string{"/tls/tls.crt": string(cert), "/tls/tls.key": string(key), "/tls/ca.crt": string(root.certPEM)},
		},
		{name: "prometheus", ip: prometheusIP, image: own["prometheus"]},
		{
			name: "alertmanager", ip: alertmanagerIP, image: own["alertmanager"],
			files: map[string]string{"/etc/ssl/certs/ca-certificates.crt": string(root.certPEM)},
		},
		{
			name: "grafana", ip: grafanaIP, image: own["grafana"],
			opts: []testcontainers.ContainerCustomizer{testcontainers.WithEnv(map[string]string{"GF_SECURITY_ADMIN_PASSWORD": grafanaPassword})},
		},
	}
	for _, c := range containers {
		if _, err := l.run(ctx, c); err != nil {
			return err
		}
	}
	return (&monitoring{http: &http.Client{Timeout: 20 * time.Second}}).ready(ctx)
}

// labContainer is a container of the lab, with its name as container and
// host name, at a fixed address, with the lab's DNS.
type labContainer struct {
	name, ip, image string
	// The content of each file it gets, by path, readable by everyone: the
	// services do not run as root. sshd wants its SSH files in secrets,
	// readable by root only.
	files, secrets map[string]string
	opts            []testcontainers.ContainerCustomizer
}

// run starts the container. testcontainers removes it when the test ends.
func (l *lab) run(ctx context.Context, c labContainer) (*testcontainers.DockerContainer, error) {
	ip := netip.MustParseAddr(c.ip)
	opts := []testcontainers.ContainerCustomizer{
		testcontainers.WithName(c.name),
		tcnetwork.WithNetwork([]string{c.name}, l.network),
		testcontainers.WithEndpointSettingsModifier(func(s map[string]*mobynet.EndpointSettings) {
			if e, ok := s[l.network.Name]; ok {
				e.IPAMConfig = &mobynet.EndpointIPAMConfig{IPv4Address: ip}
			}
		}),
		testcontainers.WithConfigModifier(func(config *moby.Config) { config.Hostname = c.name }),
	}
	// The DNS itself keeps Docker's resolver, to which it forwards every
	// other name.
	if c.ip != dnsIP {
		opts = append(opts, testcontainers.WithHostConfigModifier(func(h *moby.HostConfig) {
			h.DNS = []netip.Addr{netip.MustParseAddr(dnsIP)}
		}))
	}
	for path, content := range c.files {
		opts = append(opts, testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: strings.NewReader(content), ContainerFilePath: path, FileMode: 0o644,
		}))
	}
	for path, content := range c.secrets {
		opts = append(opts, testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: strings.NewReader(content), ContainerFilePath: path, FileMode: 0o600,
		}))
	}
	ctr, err := testcontainers.Run(ctx, c.image, append(opts, c.opts...)...)
	testcontainers.CleanupContainer(l.t, ctr)
	if ctr != nil {
		// Cleanups run last first, so this one runs before the container
		// goes.
		l.t.Cleanup(func() {
			if l.t.Failed() {
				l.printLog(c.name, ctr)
			}
		})
	}
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", c.name, err)
	}
	return ctr, nil
}

// execIn runs a command in a container and returns its exit code and its
// output, stdout and stderr together.
func execIn(ctx context.Context, c *testcontainers.DockerContainer, cmd ...string) (int, string, error) {
	code, reader, err := c.Exec(ctx, cmd)
	if err != nil {
		return 0, "", fmt.Errorf("%s: %w", strings.Join(cmd, " "), err)
	}
	out, err := io.ReadAll(reader)
	return code, string(out), err
}

// printLog prints the end of a container's log, for a failed test.
func (l *lab) printLog(name string, c *testcontainers.DockerContainer) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	logs, err := c.Logs(ctx)
	var out []byte
	if err == nil {
		out, err = io.ReadAll(logs)
		err = errors.Join(err, logs.Close())
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	lines = lines[max(0, len(lines)-40):]
	if err != nil {
		lines = append(lines, err.Error())
	}
	l.t.Logf("the end of the log of %s:\n%s", name, strings.Join(lines, "\n"))
}
