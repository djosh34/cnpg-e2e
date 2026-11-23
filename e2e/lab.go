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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

// The lab: the services outside k3s, three nodes and the controller, each
// a container at a fixed address on one network without internet. In the
// KVM scenarios each node is a VM in its container (images/vm). This
// machine is the network's gateway and reaches them all. Ryuk removes the
// containers when the run ends.
const (
	subnet       = "10.77.0.0/24"
	gatewayIP    = "10.77.0.1"
	dnsIP        = "10.77.0.2"
	controllerIP = "10.77.0.5"
	s3IP         = "10.77.0.20"
	registryIP   = "10.77.0.21"
	acmeIP       = "10.77.0.30"
	clientIP     = "10.77.0.40"

	domain     = "example.test"
	s3Name     = "s3.example.test"
	acmeName   = "acme.example.test"
	clientName = "orders-app.test"

	nodeCount = 3

	// The writable registry that fill copies the images into, on the
	// store's registry bucket, which the registry of every node reads.
	registryAddr       = registryIP + ":5000"
	registryBucket     = "registry"
	registryAccess     = "example-registry-access-key"
	registrySecret     = "example-registry-secret-key"
	registryHTTPSecret = "example-registry-secret"

	// k3s's containerd cannot keep its snapshots in the node's overlay
	// layer, and runc cannot idmap a mount of it for the instances' user
	// namespaces, so these are volumes.
	containerdDir = "/var/lib/rancher/k3s/agent/containerd"
	kubeletDir    = "/var/lib/kubelet"
	storageDir    = "/var/lib/rancher/k3s/storage"
)

func nodeName(i int) string { return fmt.Sprintf("k3s-%d", i+1) }
func nodeHost(i int) string { return nodeName(i) + "." + domain }
func nodeIP(i int) string   { return fmt.Sprintf("10.77.0.%d", 11+i) }

type lab struct {
	vms        bool
	network    *testcontainers.DockerNetwork
	containers map[string]*testcontainers.DockerContainer
	controller *testcontainers.DockerContainer
	root       *ca
	bao        *openbao
	// The charts' secrets, the environment of install.yml.
	env     map[string]string
	mon     *monitoring
	started time.Time
}

// startLab starts the lab and checks that nothing in it reaches the
// internet. With from, the store, the CA and the nodes start from the
// images of a saved cluster, with what it saved. A lab that fails to start
// is removed.
func startLab(ctx context.Context, m *versions.Manifest, vms bool, from *cached) (_ *lab, err error) {
	l := &lab{vms: vms, containers: map[string]*testcontainers.DockerContainer{}, started: time.Now()}
	defer func() {
		if err != nil {
			err = errors.Join(err, l.close(context.WithoutCancel(ctx)))
		}
	}()
	lab := m.Lab
	images := map[string]string{
		"dns": lab.DNS, "acme": lab.ACME, "s3": lab.S3, "otel": lab.OTel,
		"prometheus": lab.Prometheus, "alertmanager": lab.Alertmanager, "grafana": lab.Grafana,
	}
	logf("building the images of the controller and the nodes")
	if images["controller"], err = build(ctx, "controller", "BASE="+lab.Controller, "ANSIBLE_CORE="+lab.AnsibleCore); err != nil {
		return nil, err
	}
	node := ""
	switch {
	case vms:
		node, err = build(ctx, "vm", "BASE="+lab.VM, "DISK_URL="+lab.VMDisk.URL, "DISK_SHA256="+lab.VMDisk.SHA256)
	case from == nil:
		node, err = build(ctx, "node", "BASE="+lab.Node)
	}
	if err != nil {
		return nil, err
	}
	for i := range nodeCount {
		images[nodeName(i)] = node
	}
	if from != nil {
		for name, image := range from.images {
			images[name] = image
		}
		if l.root, err = parseCA([]byte(from.CACert), []byte(from.CAKey)); err != nil {
			return nil, err
		}
		l.env = from.Env
	} else if l.root, err = newCA(); err != nil {
		return nil, err
	}
	if !vms {
		if err := prepareHost(ctx, images[nodeName(0)]); err != nil {
			return nil, err
		}
	}
	l.network, err = tcnetwork.New(ctx, tcnetwork.WithInternal(), tcnetwork.WithIPAM(&mobynet.IPAM{
		Config: []mobynet.IPAMConfig{{Subnet: netip.MustParsePrefix(subnet), Gateway: netip.MustParseAddr(gatewayIP)}},
	}))
	if err != nil {
		return nil, err
	}

	logf("starting the DNS, the company CA, the store and the registry")
	_, err = l.run(ctx, labContainer{
		name: "dns", ip: dnsIP, image: images["dns"], dockerDNS: true, cmd: []string{"-conf", "/etc/coredns/Corefile"},
		files: map[string]string{"/etc/coredns/Corefile": corefile, "/etc/coredns/hosts": hosts()},
	})
	if err != nil {
		return nil, err
	}
	if err := l.runACME(ctx, images["acme"], from); err != nil {
		return nil, err
	}
	if err := l.runStore(ctx, images["s3"], from != nil); err != nil {
		return nil, err
	}
	if err := l.runRegistry(ctx, m.Registry.Image); err != nil {
		return nil, err
	}
	if l.env == nil {
		if l.env, err = l.chartSecrets(ctx); err != nil {
			return nil, err
		}
	}
	logf("starting the monitoring")
	if l.mon, err = l.runMonitoring(ctx, images); err != nil {
		return nil, err
	}

	logf("starting the nodes and the controller")
	key, authorized, err := sshKey()
	if err != nil {
		return nil, err
	}
	for i := range nodeCount {
		if vms {
			err = l.runVM(ctx, i, images[nodeName(i)], authorized)
		} else {
			err = l.runNode(ctx, i, images[nodeName(i)], authorized)
		}
		if err != nil {
			return nil, err
		}
	}
	repo, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	l.controller, err = l.run(ctx, labContainer{
		name: "controller", ip: controllerIP, image: images["controller"],
		files:   map[string]string{"/etc/ansible/hosts": inventory(vms)},
		secrets: map[string]string{"/root/.ssh/id_ed25519": key},
		opts: []testcontainers.ContainerCustomizer{
			// The checkout, where fill put files/.
			testcontainers.WithHostConfigModifier(func(h *moby.HostConfig) {
				h.Mounts = append(h.Mounts, mount.Mount{Type: mount.TypeBind, Source: repo, Target: "/repo", ReadOnly: true})
			}),
			testcontainers.WithEnv(map[string]string{
				"ANSIBLE_CONFIG": "/repo/ansible/ansible.cfg",
				// The nodes are new on every run.
				"ANSIBLE_HOST_KEY_CHECKING": "False",
			}),
		},
	})
	if err != nil {
		return nil, err
	}
	if err := l.airgapped(ctx); err != nil {
		return nil, err
	}
	return l, nil
}

// close removes the lab's containers and network, and stops the receiver.
func (l *lab) close(ctx context.Context) error {
	var errs []error
	for _, c := range l.containers {
		errs = append(errs, testcontainers.TerminateContainer(c))
	}
	if l.network != nil {
		errs = append(errs, l.network.Remove(ctx))
	}
	if l.mon != nil {
		errs = append(errs, l.mon.close())
	}
	return errors.Join(errs...)
}

// build builds images/<name> with the build args, and returns its tag.
func build(ctx context.Context, name string, args ...string) (string, error) {
	tag := "cnpg-e2e-" + name
	cmd := []string{"build", "--tag", tag}
	for _, a := range args {
		cmd = append(cmd, "--build-arg", a)
	}
	out, err := exec.CommandContext(ctx, "docker", append(cmd, filepath.Join("images", name))...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker build images/%s: %w\n%s", name, err, out)
	}
	return tag, nil
}

// hostSetup loads br_netfilter, which the nodes need and cannot load
// themselves, and raises fs.inotify.max_user_instances, which is the
// machine's for every container and 128 on many.
const hostSetup = `set -eu
modprobe br_netfilter
if [ "$(sysctl -n fs.inotify.max_user_instances)" -lt 1024 ]; then
  sysctl -w fs.inotify.max_user_instances=1024
fi`

// prepareHost runs hostSetup in this machine's mount namespace, from a
// privileged container of the node image.
func prepareHost(ctx context.Context, node string) error {
	_, err := command(ctx, "docker", "run", "--rm", "--privileged", "--pid=host", node,
		"nsenter", "--target", "1", "--mount", "--", "sh", "-c", hostSetup)
	return err
}

// The inventory of the controller. The nodes' own resolv.conf names
// Docker's resolver on their loopback, so k3s gets the lab's DNS.
func inventory(vms bool) string {
	lines := []string{"[nodes]"}
	for i := range nodeCount {
		lines = append(lines, nodeHost(i)+" ansible_host="+nodeIP(i))
	}
	return strings.Join(append(lines, "", "[nodes:vars]", "ansible_user=root", "dns_server="+dnsIP,
		fmt.Sprintf("selinux=%t", vms)), "\n") + "\n"
}

// The DNS: the lab's names, and Docker's resolver for every other.
const corefile = `.:53 {
    hosts /etc/coredns/hosts {
        fallthrough
    }
    forward . /etc/resolv.conf
    errors
}
`

func hosts() string {
	lines := []string{
		s3IP + " " + s3Name, acmeIP + " " + acmeName, clientIP + " " + clientName,
		otelIP + " " + otelName, prometheusIP + " " + prometheusName,
		alertmanagerIP + " " + alertmanagerName, grafanaIP + " " + grafanaName,
		gatewayIP + " " + receiverName,
	}
	for i := range nodeCount {
		lines = append(lines, nodeIP(i)+" "+nodeHost(i))
	}
	return strings.Join(lines, "\n") + "\n"
}

// runNode starts node i, privileged with systemd, up once sshd and the
// default route are.
func (l *lab) runNode(ctx context.Context, i int, image, authorized string) error {
	_, err := l.run(ctx, labContainer{
		name: nodeName(i), ip: nodeIP(i), image: image,
		secrets: map[string]string{"/root/.ssh/authorized_keys": authorized},
		opts: []testcontainers.ContainerCustomizer{
			testcontainers.WithHostConfigModifier(func(h *moby.HostConfig) {
				h.Privileged = true
				h.CgroupnsMode = moby.CgroupnsModePrivate
				h.Tmpfs = map[string]string{"/run": "", "/run/lock": "", "/tmp": ""}
				// A new volume starts with what the image has at its path:
				// a saved node's storage.
				for _, dir := range []string{containerdDir, kubeletDir, storageDir} {
					h.Mounts = append(h.Mounts, mount.Mount{Type: mount.TypeVolume, Target: dir})
				}
			}),
			testcontainers.WithWaitStrategy(wait.ForExec([]string{"sh", "-c", "systemctl is-active sshd && systemctl is-active default-route"}).
				WithStartupTimeout(2 * time.Minute)),
		},
	})
	return err
}

// What a VM prints on its serial console once sshd runs with the key.
const vmUp = "e2e: the VM is up"

// runVM starts node i as a VM, whose cloud-init seed gives it the node's
// name and address, a default route, the lab's DNS and root the key.
func (l *lab) runVM(ctx context.Context, i int, image, authorized string) error {
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
	_, err := l.run(ctx, labContainer{
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
			testcontainers.WithWaitStrategyAndDeadline(5*time.Minute, wait.ForLog(vmUp).WithStartupTimeout(5*time.Minute)),
		},
	})
	return err
}

// sshKey is a new key of the controller and the nodes' authorized_keys
// line.
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

// airgapped checks that neither the controller nor a node reaches the
// internet: bash exits with 1 when it cannot connect, timeout with 124.
func (l *lab) airgapped(ctx context.Context) error {
	connect := []string{"timeout", "10", "bash", "-c", "exec 3<>/dev/tcp/1.1.1.1/443"}
	code, out, err := execIn(ctx, l.controller, connect)
	if err == nil && code != 1 && code != 124 {
		err = fmt.Errorf("the controller reaches the internet: exit code %d %s", code, out)
	}
	if err != nil {
		return err
	}
	for i := range nodeCount {
		code, out, err := l.onNode(ctx, i, connect...)
		if err == nil && code != 1 && code != 124 {
			err = fmt.Errorf("%s reaches the internet: exit code %d %s", nodeName(i), code, out)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// onNode runs a command on node i over SSH from the controller, as Ansible
// does, and returns its exit code, 255 when SSH fails, and its output.
func (l *lab) onNode(ctx context.Context, i int, cmd ...string) (int, string, error) {
	quoted := make([]string, len(cmd))
	for j, arg := range cmd {
		quoted[j] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return execIn(ctx, l.controller, []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR", "root@" + nodeIP(i), strings.Join(quoted, " ")})
}

// playbook runs ansible-playbook on the controller with the charts'
// secrets and returns its output.
func (l *lab) playbook(ctx context.Context, env map[string]string, args ...string) (string, error) {
	cmd := append([]string{"ansible-playbook"}, args...)
	vars := make([]string, 0, len(env))
	for k, v := range env {
		vars = append(vars, k+"="+v)
	}
	code, out, err := execIn(ctx, l.controller, cmd, tcexec.WithWorkingDir("/repo/ansible"), tcexec.WithEnv(vars))
	if err == nil && code != 0 {
		err = fmt.Errorf("%s: exit code %d\n%s", strings.Join(cmd, " "), code, out)
	}
	return out, err
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

// runACME starts the company CA, OpenBao, and sets it up, or unseals the
// saved one.
func (l *lab) runACME(ctx context.Context, image string, from *cached) error {
	cert, key, err := l.root.serverCert(acmeName)
	if err != nil {
		return err
	}
	_, err = l.run(ctx, labContainer{
		name: "acme", ip: acmeIP, image: image, cmd: []string{"server", "-config=/bao/bao.hcl"},
		files: map[string]string{"/bao/bao.hcl": baoConfig, "/bao/tls.crt": string(cert), "/bao/tls.key": string(key)},
	})
	if err != nil {
		return err
	}
	if from == nil {
		l.bao, err = setUpACME(ctx, l.root)
		return err
	}
	l.bao = &openbao{http: &http.Client{Timeout: 2 * time.Minute}, token: from.Token, unseal: from.Unseal}
	return l.bao.unsealed(ctx)
}

// OpenBao's API without TLS on 8200, for the e2e; ACME on 443.
const baoConfig = `storage "raft" {
  path    = "/openbao/file"
  node_id = "acme"
}
cluster_addr = "http://127.0.0.1:8201"
api_addr     = "https://acme.example.test"
listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
}
listener "tcp" {
  address       = "0.0.0.0:443"
  tls_cert_file = "/bao/tls.crt"
  tls_key_file  = "/bao/tls.key"
}
`

// runStore starts the store and sets it up with the registry's bucket and
// a user that may only read it, or waits until the saved one answers. Its
// data is in the container's layer, its metadata in sidecar files, so
// docker commit keeps both.
func (l *lab) runStore(ctx context.Context, image string, saved bool) error {
	cert, key, err := l.root.serverCert(s3Name)
	if err != nil {
		return err
	}
	_, err = l.run(ctx, labContainer{
		name: "s3", ip: s3IP, image: image,
		files: map[string]string{"/tls/tls.crt": string(cert), "/tls/tls.key": string(key)},
		opts: []testcontainers.ContainerCustomizer{
			testcontainers.WithEntrypoint("sh", "-c", `mkdir -p /s3/data /s3/iam /s3/versions /s3/meta && exec /usr/local/bin/versitygw "$@"`, "sh"),
			testcontainers.WithCmd(s3Args...),
		},
	})
	if err != nil {
		return err
	}
	rootKeys := newS3(l.root, s3RootAccess, s3RootSecret)
	if saved {
		return poll(ctx, time.Now().Add(time.Minute), func(ctx context.Context) error {
			return rootKeys.want(ctx, http.MethodGet, "/", "", 200)
		})
	}
	if err := setUpStore(ctx, rootKeys, newS3(l.root, s3Access, s3Secret)); err != nil {
		return err
	}
	if err := rootKeys.want(ctx, http.MethodPut, "/"+registryBucket, "", 200); err != nil {
		return err
	}
	if err := s3User(ctx, registryAccess, registrySecret); err != nil {
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

// The writable registry fill copies into: the registry bucket over the
// store's TLS, without redirects, so its clients never need the store.
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

// runRegistry starts the writable registry, on the image the nodes run.
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
	return poll(ctx, time.Now().Add(time.Minute), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+registryAddr+"/v2/", nil)
		if err != nil {
			return err
		}
		return callJSON(client, req, nil)
	})
}

// chartSecrets are the company CA and the charts' secrets: the store's keys
// of the registry and of the backups, and a new EAB key of the company CA
// for each ACME account.
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

// labContainer is a container of the lab with its name as host name, at a
// fixed address, with the lab's DNS.
type labContainer struct {
	name, ip, image string
	cmd             []string
	// Files by path, readable by everyone: the services do not run as
	// root. sshd wants its files in secrets, readable by root only.
	files, secrets map[string]string
	// Files by path, from this machine, readable and executable by everyone.
	hostFiles map[string]string
	// Docker's resolver, for the DNS itself.
	dockerDNS bool
	opts      []testcontainers.ContainerCustomizer
}

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
	if c.cmd != nil {
		opts = append(opts, testcontainers.WithCmd(c.cmd...))
	}
	if !c.dockerDNS {
		opts = append(opts, testcontainers.WithHostConfigModifier(func(h *moby.HostConfig) {
			h.DNS = []netip.Addr{netip.MustParseAddr(dnsIP)}
		}))
	}
	var files []testcontainers.ContainerFile
	for path, content := range c.files {
		files = append(files, testcontainers.ContainerFile{Reader: strings.NewReader(content), ContainerFilePath: path, FileMode: 0o644})
	}
	for path, content := range c.secrets {
		files = append(files, testcontainers.ContainerFile{Reader: strings.NewReader(content), ContainerFilePath: path, FileMode: 0o600})
	}
	for path, host := range c.hostFiles {
		files = append(files, testcontainers.ContainerFile{HostFilePath: host, ContainerFilePath: path, FileMode: 0o755})
	}
	opts = append(opts, testcontainers.WithFiles(files...))
	ctr, err := testcontainers.Run(ctx, c.image, append(opts, c.opts...)...)
	if ctr != nil {
		l.containers[c.name] = ctr
	}
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", c.name, err)
	}
	return ctr, nil
}
