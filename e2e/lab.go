package e2e

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
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
)

// The lab is what the e2e test starts on the machine's own Docker, with only
// Go and Docker: the services outside k3s, three nodes and the controller,
// each a container at a fixed address on one network with no route to the
// internet. In the KVM scenarios each node is a VM in its container, which
// takes the container's address (images/vm). The machine itself reaches
// them on that network, and the controller also has a network to the
// internet, as the machine that runs Ansible in production. The images are
// this repo's packages, or built here where a package does not have one yet
// (images.Get). testcontainers removes every container and the network when
// the test ends, and Ryuk does when the test is killed.
const (
	// The lab's network, and the machine's own address on it.
	subnet    = "10.77.0.0/24"
	gatewayIP = "10.77.0.1"
	// The writable registry that the fill copies the images into, on the
	// store's registry bucket, which the registry of every node reads too.
	registryIP     = "10.77.0.21"
	registryBucket = "registry"
	// The keys of the registry of every node, which may only read the
	// registry bucket.
	registryAccess = "example-registry-access-key"
	registrySecret = "example-registry-secret-key"
	// http.secret, which the writable registry and the registry of every
	// node share.
	registryHTTPSecret = "example-registry-secret"
	// The machine that runs Ansible.
	controllerIP = "10.77.0.5"
	// Where the controller has the checkout's ansible/, next to helm/ and
	// versions/ as in the checkout. testcontainers copies a directory only
	// into one that is there, such as /.
	ansibleDir         = "/ansible"
	controllerVersions = "/versions/versions.yaml"
	// k3s's containerd keeps its images and snapshots here. It cannot keep
	// them in the node's own layer, where the overlay mounts of its
	// snapshots fail, so a volume holds them.
	containerdDir = "/var/lib/rancher/k3s/agent/containerd"
	kubeletDir    = "/var/lib/kubelet"
)

var registryAddr = registryIP + ":5000"

// The versions file of the checkout.
const versionsFile = "../versions/versions.yaml"

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
  secret: ` + registryHTTPSecret + `
`

// The inventory of the controller's Ansible: the nodes by name and address,
// and the lab's DNS for k3s, since the nodes' own resolv.conf names
// Docker's resolver on their loopback. VMs, which have SELinux enforcing,
// get selinux on.
func inventory(vms bool) string {
	lines := []string{"[nodes]"}
	for i := range nodeCount {
		lines = append(lines, nodeHost(i)+" ansible_host="+nodeIP(i))
	}
	lines = append(lines, "", "[nodes:vars]", "ansible_user=root", "dns_server="+dnsIP)
	if vms {
		lines = append(lines, "selinux=true")
	}
	return strings.Join(lines, "\n") + "\n"
}

type lab struct {
	t          *testing.T
	network    *testcontainers.DockerNetwork
	root       *ca
	bao        *openbao
	nodes      []*testcontainers.DockerContainer
	controller *testcontainers.DockerContainer
	// Every container by its name, and the image of each.
	containers map[string]*testcontainers.DockerContainer
	images     map[string]string
}

// startLab starts the lab: the services outside k3s, set up and answering,
// three nodes with sshd running, and the controller with an SSH key that
// every node takes. Without a cache the nodes are empty and the store and
// the company CA new. From a cache the store, the CA and the nodes are its
// images: the CA's root is the cache's, OpenBao is unsealed with its key,
// and the store and the CA get new TLS certificates. With vms the nodes are
// new VMs, never from a cache.
func startLab(ctx context.Context, t *testing.T, m *versions.Manifest, from *cache, vms bool) (*lab, error) {
	l := &lab{t: t, containers: map[string]*testcontainers.DockerContainer{}}
	// The image of each container: the cache's where it holds the
	// container, else this repo's image of its kind.
	kinds := map[string]string{}
	for _, name := range []string{"dns", "acme", "s3", "otel", "prometheus", "alertmanager", "grafana", "controller"} {
		kinds[name] = name
	}
	for i := range nodeCount {
		kinds[nodeName(i)] = "node"
		if vms {
			kinds[nodeName(i)] = "vm"
		}
	}
	image := map[string]string{}
	l.images = image
	var err error
	for name, kind := range kinds {
		if from != nil && from.images[name] != "" {
			image[name] = from.images[name]
		} else if image[name], err = images.Get(ctx, kind); err != nil {
			return nil, err
		}
	}
	if from == nil {
		if l.root, err = newCA(); err != nil {
			return nil, err
		}
	} else {
		l.root = from.root
	}
	n, err := tcnetwork.New(ctx, tcnetwork.WithInternal(), tcnetwork.WithIPAM(&mobynet.IPAM{
		Config: []mobynet.IPAMConfig{{Subnet: netip.MustParsePrefix(subnet), Gateway: netip.MustParseAddr(gatewayIP)}},
	}))
	testcontainers.CleanupNetwork(t, n)
	if err != nil {
		return nil, err
	}
	l.network = n

	logf("starting the DNS, the company CA, the store and the registry")
	_, err = l.run(ctx, labContainer{name: "dns", ip: dnsIP, image: image["dns"], dockerDNS: true, files: map[string]string{"/etc/coredns/hosts": hosts()}})
	if err != nil {
		return nil, err
	}
	if err := l.runACME(ctx, image["acme"], from); err != nil {
		return nil, err
	}
	if err := l.runStore(ctx, image["s3"], from != nil); err != nil {
		return nil, err
	}
	if err := l.runRegistry(ctx, m.Registry.Image); err != nil {
		return nil, err
	}
	logf("starting the monitoring")
	if err := l.runMonitoring(ctx, image); err != nil {
		return nil, err
	}
	logf("starting the nodes and the controller")
	key, authorized, err := sshKey()
	if err != nil {
		return nil, err
	}
	for i := range nodeCount {
		if vms {
			node, err := l.runVM(ctx, i, image[nodeName(i)], authorized)
			if err != nil {
				return nil, err
			}
			l.nodes = append(l.nodes, node)
			continue
		}
		node, err := l.run(ctx, labContainer{
			name: nodeName(i), ip: nodeIP(i), image: image[nodeName(i)],
			secrets: map[string]string{"/root/.ssh/authorized_keys": authorized},
			// A node is up once systemd started sshd and the default route.
			opts: []testcontainers.ContainerCustomizer{
				testcontainers.WithHostConfigModifier(func(h *moby.HostConfig) {
					h.Privileged = true
					h.CgroupnsMode = moby.CgroupnsModePrivate
					h.Tmpfs = map[string]string{"/run": "", "/run/lock": "", "/tmp": ""}
					// The kubelet's pod volumes and k3s's local-path volumes,
					// such as the instances' data, on volumes too: the
					// instances run in a user namespace, and runc cannot
					// idmap a mount of the node's own overlay layer.
					h.Mounts = append(h.Mounts, mount.Mount{Type: mount.TypeVolume, Target: containerdDir},
						mount.Mount{Type: mount.TypeVolume, Target: kubeletDir},
						mount.Mount{Type: mount.TypeVolume, Target: storageDir})
				}),
				// systemctl is-active of two units holds when either is.
				testcontainers.WithWaitStrategy(wait.ForExec([]string{"sh", "-c", "systemctl is-active sshd && systemctl is-active default-route"}).
					WithStartupTimeout(2 * time.Minute)),
			},
		})
		if err != nil {
			return nil, err
		}
		l.nodes = append(l.nodes, node)
	}
	l.controller, err = l.run(ctx, labContainer{
		name: "controller", ip: controllerIP, image: image["controller"], dockerDNS: true,
		files:   map[string]string{"/etc/ansible/hosts": inventory(vms)},
		secrets: map[string]string{"/root/.ssh/id_ed25519": key},
		opts: []testcontainers.ContainerCustomizer{
			// To the internet, for the downloads of the playbooks.
			tcnetwork.WithBridgeNetwork(),
			testcontainers.WithFiles(
				testcontainers.ContainerFile{HostFilePath: "../ansible", ContainerFilePath: ansibleDir, FileMode: 0o755},
				testcontainers.ContainerFile{HostFilePath: versionsFile, ContainerFilePath: controllerVersions, FileMode: 0o644},
				testcontainers.ContainerFile{HostFilePath: "../helm", ContainerFilePath: "/helm", FileMode: 0o755},
			),
			// The nodes are new on every run.
			testcontainers.WithEnv(map[string]string{
				"ANSIBLE_HOST_KEY_CHECKING": "False",
				"ANSIBLE_CONFIG":            ansibleDir + "/ansible.cfg",
			}),
		},
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

// What a VM prints on its serial console once cloud-init gave root the
// controller's key and sshd runs.
const vmUp = "e2e: the VM is up"

// runVM starts node i as a VM in a container of image, of images/vm, and
// waits until the VM is up. Its cloud-init seed gives it the node's name and
// address, a default route through the network's gateway as a machine of
// production has, the lab's DNS, and root the controller's key.
func (l *lab) runVM(ctx context.Context, i int, image, authorized string) (*testcontainers.DockerContainer, error) {
	mac := fmt.Sprintf("52:54:00:00:00:%02x", netip.MustParseAddr(nodeIP(i)).As4()[3])
	userData := `#cloud-config
disable_root: false
ssh_authorized_keys:
  - ` + strings.TrimSpace(authorized) + `
runcmd:
  - "until systemctl is-active --quiet sshd; do sleep 1; done; echo '` + vmUp + `' >/dev/ttyS0"
`
	networkConfig := fmt.Sprintf(`version: 2
ethernets:
  eth0:
    match:
      macaddress: "%s"
    set-name: eth0
    addresses: [%s/%d]
    gateway4: %s
    nameservers:
      addresses: [%s]
`, mac, nodeIP(i), netip.MustParsePrefix(subnet).Bits(), gatewayIP, dnsIP)
	return l.run(ctx, labContainer{
		name: nodeName(i), ip: nodeIP(i), image: image,
		files: map[string]string{
			"/seed/meta-data":      "instance-id: " + nodeName(i) + "\nlocal-hostname: " + nodeName(i) + "\n",
			"/seed/user-data":      userData,
			"/seed/network-config": networkConfig,
		},
		opts: []testcontainers.ContainerCustomizer{
			testcontainers.WithEnv(map[string]string{"MAC": mac}),
			testcontainers.WithHostConfigModifier(func(h *moby.HostConfig) {
				h.Privileged = true
				h.Mounts = append(h.Mounts, mount.Mount{Type: mount.TypeVolume, Target: "/disk"})
			}),
			// Both the strategy and testcontainers' wait for all strategies
			// give up after 1 min by default.
			testcontainers.WithWaitStrategyAndDeadline(5*time.Minute, wait.ForLog(vmUp).WithStartupTimeout(5*time.Minute)),
		},
	})
}

// onNode runs a command on node i over SSH from the controller, as Ansible
// reaches the nodes, and returns its exit code and its output, stdout and
// stderr together. The exit code is 255 when SSH fails.
func (l *lab) onNode(ctx context.Context, i int, cmd ...string) (int, string, error) {
	quoted := make([]string, len(cmd))
	for j, arg := range cmd {
		quoted[j] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return execIn(ctx, l.controller, []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR", "root@" + nodeIP(i), strings.Join(quoted, " ")})
}

// runACME starts the company CA and sets it up, or unseals the cache's.
func (l *lab) runACME(ctx context.Context, image string, from *cache) error {
	cert, key, err := l.root.serverCert(acmeName)
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
	if from == nil {
		l.bao, err = setUpACME(ctx, l.root)
		return err
	}
	l.bao = &openbao{http: &http.Client{Timeout: 2 * time.Minute}, token: from.token, unseal: from.unseal}
	return l.bao.unsealed(ctx)
}

// runStore starts the store, then sets it up and makes the registry's
// bucket, with a user that may only read it, or, restored, waits until it
// answers and sets the buckets' settings again. Its data is in the
// container's own layer, so that docker commit keeps it.
func (l *lab) runStore(ctx context.Context, image string, restored bool) error {
	cert, key, err := l.root.serverCert(s3Name)
	if err != nil {
		return err
	}
	_, err = l.run(ctx, labContainer{
		name: "s3", ip: s3IP, image: image,
		files: map[string]string{"/tls/tls.crt": string(cert), "/tls/tls.key": string(key)},
		opts: []testcontainers.ContainerCustomizer{
			testcontainers.WithEntrypoint("/usr/local/bin/versitygw"),
			testcontainers.WithCmd(s3Args...),
		},
	})
	if err != nil {
		return err
	}
	rootKeys := newS3(l.root, s3RootAccess, s3RootSecret)
	if restored {
		err := poll(ctx, time.Now().Add(60*time.Second), func(ctx context.Context) error {
			return rootKeys.want(ctx, http.MethodGet, "/", "", 200)
		})
		if err != nil {
			return fmt.Errorf("the store answering: %w", err)
		}
		// versitygw keeps a bucket's versioning and policy in extended
		// attributes, which docker commit leaves out.
		for _, st := range []struct {
			path, body string
			status     int
		}{
			{"/" + s3Bucket + "?versioning", versioningOn, 200},
			{"/" + s3Bucket + "?policy", s3Policy, 204},
			{"/" + registryBucket + "?policy", registryPolicy, 204},
		} {
			if err := rootKeys.want(ctx, http.MethodPut, st.path, st.body, st.status); err != nil {
				return fmt.Errorf("the restored store's settings: %w", err)
			}
		}
		return nil
	}
	if err := setUpStore(ctx, l.root); err != nil {
		return err
	}
	if err := rootKeys.want(ctx, http.MethodPut, "/"+registryBucket, "", 200); err != nil {
		return err
	}
	_, err = command(ctx, "docker", "exec", "s3", "/usr/local/bin/versitygw", "admin", "--access", s3RootAccess,
		"--secret", s3RootSecret, "--endpoint-url", s3Admin, "create-user", "--access", registryAccess, "--secret", registrySecret, "--role", "user")
	if err != nil {
		return err
	}
	return rootKeys.want(ctx, http.MethodPut, "/"+registryBucket+"?policy", registryPolicy, 204)
}

// registryPolicy lets the registry of every node list and read the
// registry bucket, and nothing else.
var registryPolicy = `{"Version": "2012-10-17", "Statement": [
  {"Effect": "Allow", "Principal": {"AWS": ["` + registryAccess + `"]}, "Action": "s3:ListBucket",
   "Resource": "arn:aws:s3:::` + registryBucket + `"},
  {"Effect": "Allow", "Principal": {"AWS": ["` + registryAccess + `"]}, "Action": "s3:GetObject",
   "Resource": "arn:aws:s3:::` + registryBucket + `/*"}
]}`

// runRegistry starts the writable registry on the registry bucket, with
// the registry image of the versions file, and waits until it answers.
func (l *lab) runRegistry(ctx context.Context, image string) error {
	_, err := l.run(ctx, labContainer{
		name: "registry", ip: registryIP, image: image,
		files: map[string]string{"/etc/distribution/config.yml": registryConfig, "/etc/distribution/ca.crt": string(l.root.certPEM)},
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

// runMonitoring starts the OTel service, Prometheus, Alertmanager and
// Grafana, and waits until each answers.
func (l *lab) runMonitoring(ctx context.Context, image map[string]string) error {
	cert, key, err := l.root.serverCert(otelName)
	if err != nil {
		return err
	}
	containers := []labContainer{
		{
			name: "otel", ip: otelIP, image: image["otel"],
			files: map[string]string{"/tls/tls.crt": string(cert), "/tls/tls.key": string(key), "/tls/ca.crt": string(l.root.certPEM)},
		},
		{name: "prometheus", ip: prometheusIP, image: image["prometheus"]},
		{
			name: "alertmanager", ip: alertmanagerIP, image: image["alertmanager"],
			files: map[string]string{"/etc/ssl/certs/ca-certificates.crt": string(l.root.certPEM)},
		},
		{
			name: "grafana", ip: grafanaIP, image: image["grafana"],
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
	// It keeps Docker's resolver: the DNS itself, which forwards every
	// other name there, and the controller, which resolves the internet's
	// names.
	dockerDNS bool
	opts      []testcontainers.ContainerCustomizer
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
	if !c.dockerDNS {
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
		l.containers[c.name] = ctr
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

// playbook runs ansible-playbook with args on the controller, from the
// checkout's ansible/, with env as its environment besides the
// controller's, and returns its output.
func (l *lab) playbook(ctx context.Context, env map[string]string, args ...string) (string, error) {
	cmd := append([]string{"ansible-playbook"}, args...)
	vars := make([]string, 0, len(env))
	for k, v := range env {
		vars = append(vars, k+"="+v)
	}
	code, out, err := execIn(ctx, l.controller, cmd, tcexec.WithWorkingDir(ansibleDir), tcexec.WithEnv(vars))
	if err != nil {
		return out, err
	}
	if code != 0 {
		return out, fmt.Errorf("%s: exit code %d\n%s", strings.Join(cmd, " "), code, out)
	}
	return out, nil
}

// execIn runs a command in a container and returns its exit code and its
// output, stdout and stderr together.
func execIn(ctx context.Context, c *testcontainers.DockerContainer, cmd []string, opts ...tcexec.ProcessOption) (int, string, error) {
	code, reader, err := c.Exec(ctx, cmd, append(opts, tcexec.Multiplexed())...)
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

// hostSetup loads br_netfilter, which the nodes need and cannot load
// themselves, and raises fs.inotify.max_user_instances to 1024: the limit
// is the machine's, for every container alike, many machines have 128, and
// each node takes many.
const hostSetup = `set -eu
modprobe br_netfilter
have=$(sysctl -n fs.inotify.max_user_instances)
if [ "$have" -lt 1024 ]; then
  sysctl -w fs.inotify.max_user_instances=1024
fi`

// prepareHost runs hostSetup with the machine's own modprobe and sysctl,
// from a privileged container in the machine's mount namespace, so a run
// needs nothing on the machine besides Go and Docker. The changes hold until
// the machine reboots. node is an image of the nodes, which has nsenter: a
// fresh one, or a node of the cache.
func prepareHost(ctx context.Context, node string) error {
	_, err := command(ctx, "docker", "run", "--rm", "--privileged", "--pid=host", node,
		"nsenter", "--target", "1", "--mount", "--", "sh", "-c", hostSetup)
	return err
}
