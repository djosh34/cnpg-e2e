# The playbooks

Three Ansible playbooks in `ansible/` install and change k3s on the hosts of the inventory group `nodes`. Every node is a server with embedded etcd. The playbooks work on one node at a time.

| Playbook | What it does |
|---|---|
| `install.yml` | Installs k3s. The first node starts the cluster and each other node joins it. Then it installs Helm, its diff plugin and helmfile, and runs one `helmfile apply` from the first node ([the reconcile](reconcile.md)). A second run changes nothing and restarts nothing. |
| `upgrade.yml` | Brings a running cluster to the files of the checkout, then runs the same `helmfile apply`. It restarts k3s only on a node whose binary or config changed. It stops on a node without k3s. |
| `rotate-ca.yml` | Rotates the CAs that k3s made itself, as [k3s's docs](https://docs.k3s.io/cli/certificate#rotating-self-signed-ca-certificates) describe. k3s's script makes new CAs on the first node, each cross-signed by the old one. `k3s certificate rotate-ca` stores them, and k3s restarts on one node at a time. The first node needs `openssl`. |

All three use the role `k3s`. It writes `config.yaml` from `ansible/k3s/config.yaml` and the node's own settings, writes `registries.yaml` and the manifests of `ansible/k3s/manifests/`, copies the airgap files and the registry's image, and runs one CoreDNS pod per node.

The nodes need no internet. The controller downloads every file of `versions/versions.yaml` and checks its sha256. It fetches the charts and the registry's image by digest with [regctl](https://regclient.org/). Ansible copies all of them to the nodes.

## Run

```ini
[nodes]
node-1.example ansible_host=192.0.2.11
node-2.example ansible_host=192.0.2.12
node-3.example ansible_host=192.0.2.13

[nodes:vars]
ansible_user=root
```

```bash
cd ansible && ansible-playbook -i inventory.ini install.yml
```

Fill the registry before the first run, so the charts find their images ([the fill](fill.md)). `--skip-tags helmfile` installs or upgrades k3s without the charts.

## Variables

| Variable | What it does |
|---|---|
| `dns_server` | The DNS server of k3s and its pods, for nodes whose own `/etc/resolv.conf` does not work for them |
| `selinux=true` | For Rocky Linux 9.9 nodes with SELinux enforcing. Before k3s, the role installs `container-selinux` and `k3s-selinux` from the files of the versions file with every repository off, and k3s's config gets `selinux: true`. Rocky Linux 9.9 has every other package they need. Set it for the whole group. |
| `k3s_previous=true` | `install.yml` installs `k3s.previous` of the versions file, the k3s release one minor before. The k3s upgrade scenarios use it. |
| `pg_restore=true` | Adds the release `pg-restore` to the reconcile ([the reconcile](reconcile.md)) |
