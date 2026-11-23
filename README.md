# cnpg-e2e

A three-node k3s cluster with CloudNativePG, and an e2e runner that breaks it in scenarios: failover, switchover, recovery, a broken store, alerts and monitoring, and upgrades of the images and of k3s and a rotation of k3s's CAs.

## Run a scenario

```bash
go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario install
```

- **Needs** an amd64 machine with Go and a Docker that allows privileged containers. A run first loads the kernel module `br_netfilter` on the machine and raises its `fs.inotify.max_user_instances` to 1024, from a privileged container.
- **Results** go to `results/`. The test passes when the scenario passed.
- **Scenarios** are `airgap`, `install`, `install-kvm`, `k3s-upgrade`, `k3s-upgrade-kvm`, `restore` and `cache` below, and the tags in `e2e/features/`, after each of which `always.feature` runs.

`go -C e2e test ./...` without `-scenario` runs the unit tests only and starts no container.

The scenario `airgap` runs on the machine's own Docker with only Go and Docker: the services outside k3s, three empty Rocky Linux 9 nodes with systemd and sshd on a network with no route to the internet, and a controller with Ansible. It checks that no node reaches the internet, that the controller reaches every node over SSH, and fills the registry with every image of the versions file.

The scenario `install` starts the same machines with empty nodes, fills the registry and runs `install.yml`: k3s, then every chart. It checks that the database cluster is healthy, has its first backup and sends the metrics of every source to Prometheus, then that `upgrade.yml` on the running cluster changes nothing.

The scenario `install-kvm` is `install` on three Rocky Linux 9 VMs under QEMU and KVM, with SELinux enforcing, and `selinux=true` in the inventory (below). The services outside k3s and the controller stay in Docker. Each VM runs in a privileged container of `e2e/images/vm` and takes that container's address on the network, so nothing else changes. It also checks that SELinux is enforcing on every node and that k3s runs in its SELinux domain, `container_runtime_t`. It needs `/dev/kvm` on the machine; no other scenario does but `k3s-upgrade-kvm`.

The scenario `k3s-upgrade` starts like `install`, but runs `install.yml` with `k3s_previous=true`, which installs `k3s.previous` of the versions file, the k3s release one minor before. Then `upgrade.yml` brings every node to the k3s of the versions file. It checks that every node runs it, that every running container runs an image of the versions file, and the cluster as `install` does. `k3s-upgrade-kvm` does the same on the VMs of `install-kvm`.

The scenario `restore` starts the same machines from the cache: a clean k3s cluster that `install.yml` installed without its helmfile step, with no charts, and the store and company CA next to it. It checks that every node turns Ready, then fills the registry, runs `upgrade.yml` and checks the cluster as `install` does. The cache is one image per container in this repo's packages, tagged by a hash of what makes it: the node, store and CA images, the k3s and node tools of the versions file, `ansible/` but its role `helmfile`, and the Go code that sets the machines up. A run that finds no cache builds it on the machine. The scenario `cache` builds it when the packages do not have it, and with `-push` pushes it, which only CI does.

Each scenario of `e2e/features/` starts as `restore` does: from the cache, then the fill and `upgrade.yml`, and a healthy cluster with its first backup and the metrics of every source. Then it runs its lines and those of `always.feature` against the cluster: the cluster is healthy, no read or write gap is over the limit the feature file sets, no acknowledged write is lost, only the alerts the scenario names fired, the dashboard has data, and every image on the nodes is one the versions file pins. The test runs `kubectl` and `kubectl cnpg` of the versions file on the machine, which it downloads and checks against their sha256 there, and the client, the application outside the cluster, in a container next to the nodes. Each node keeps its database volumes on a Docker volume, but in `data-disk-full`, which fills a node's data disk: there every node gets a tmpfs of 1 GiB for them.

`image-upgrade` first deploys an earlier versions file instead: the `versions.yaml` of the newest release that has one, or before such a release, the newest version of `versions/versions.yaml` in the history of `origin/main` that differs from the checkout's, so it needs a clone with that history. It keeps the checkout's k3s, which takes no downgrade. Then `upgrade.yml` takes the checkout's versions file. It checks that every running container moves to an image of that file and that the releases whose manifests did not change keep their revision; the image check of `always.feature` takes the images of both files. `ca-rotation` runs `ansible/rotate-ca.yml` and checks that every node serves and uses certificates of the new CAs.

On GitHub, the `e2e` workflow runs scenarios by hand, each in its own job, after it publishes the images of `e2e/images` that are new. Blank, it runs every scenario. The cache job publishes the cache when it is new, and only the scenarios that start from it wait for it. The `ci` workflow runs lint and unit tests on every pull request. The `mirror` workflow copies the images and charts of the versions file from Docker Hardened Images into this repo's packages and uploads the versions file with their digests.

## Install k3s with Ansible

`ansible/install.yml` installs k3s on the hosts of the inventory group `nodes`, each a server with embedded etcd, one at a time. `ansible/upgrade.yml` brings a running cluster to the files of the checkout and restarts k3s only on a node whose binary or config changed. `ansible/rotate-ca.yml` rotates the CAs k3s made itself, as [k3s's docs](https://docs.k3s.io/cli/certificate#rotating-self-signed-ca-certificates) say: k3s's script makes new CAs on the first node, each cross-signed by the old one, `k3s certificate rotate-ca` stores them, and k3s restarts on one node at a time. It needs `openssl` on the first node. All three share the role `k3s`, which writes `config.yaml` (the settings of `ansible/k3s/config.yaml` and the node's own), `registries.yaml` and the manifests of `ansible/k3s/manifests/`, copies the airgap files and the registry's image, and runs one CoreDNS pod per node. `install.yml` and `upgrade.yml` then install Helm, its diff plugin and helmfile, and last run one `helmfile apply` of every chart from the first node (below). The nodes need no internet: the controller downloads every file of the versions file, and fetches the charts and the registry's image with [regctl](https://regclient.org/), each by its digest; Ansible copies them to the nodes.

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

`dns_server` sets the DNS server of k3s and its pods, where the nodes' own `/etc/resolv.conf` does not do. `selinux=true` is for nodes with SELinux enforcing, Rocky Linux 9.9 machines: before k3s, the role installs `container-selinux` and `k3s-selinux` from the files of the versions file with every repository off. Rocky Linux 9.9 has every other package they need. k3s's config gets `selinux: true`. Set it for the whole group. `--skip-tags helmfile` installs k3s alone.

## Deploy the charts

`helm/helmfile.yaml.gotmpl` holds every release, with the charts of `helm/charts/`, the upstream charts of the versions file and the values of `helm/values/`. One `helmfile apply` installs or upgrades only the releases that changed, each once the releases it needs are ready, and rolls back a release that fails. Every pod pulls its image through the registry on port 5000 of its node, a read-only [Distribution](https://distribution.github.io/distribution/) on the S3 bucket that the fill writes to. On a new cluster the registry goes first, from the image archive in the k3s images directory, then the prepull puts every image of the versions file on every node. On a running cluster the prepull goes first.

The role `helmfile` passes the company CA and the charts' secrets to helmfile as its environment, from these variables of the controller's environment, so they land on no disk:

| Variable | What it is |
|---|---|
| `COMPANY_CA` | The company CA, PEM: it signs the client certificates and the certificates of the ACME CA and the store |
| `REGISTRY_S3_ACCESS_KEY`, `REGISTRY_S3_SECRET_KEY` | Keys that may read the registry's bucket |
| `REGISTRY_HTTP_SECRET` | `http.secret` of the writable registry of the fill |
| `BACKUP_S3_ACCESS_KEY`, `BACKUP_S3_SECRET_KEY` | Keys of the backups' bucket |
| `PG_SERVER_EAB_KID`, `PG_SERVER_EAB_HMAC` | An EAB key of the ACME CA for the database's server certificate |
| `PG_REPLICATION_EAB_KID`, `PG_REPLICATION_EAB_HMAC` | One for its replication certificate |
| `OTEL_CLIENT_EAB_KID`, `OTEL_CLIENT_EAB_HMAC` | One for the OTel collectors' client certificate |
| `PG_RESTORE_SERVER_EAB_KID`, `PG_RESTORE_SERVER_EAB_HMAC`, `PG_RESTORE_REPLICATION_EAB_KID`, `PG_RESTORE_REPLICATION_EAB_HMAC` | Only with `pg_restore=true`: one each for the server and replication certificates of `pg-restore` |

`pg_restore=true` adds the release `pg-restore`: a second database cluster that restores the backups and WAL of `pg` from the store, with no LoadBalancer of its own. A later run without it leaves `pg-restore` as it is.

## Fill a registry

The nodes pull every image through a registry on the S3 store. To fill it, run a writable [Distribution](https://distribution.github.io/distribution/) on the same bucket, then copy every image of the versions file into it:

```bash
go -C e2e run ./cmd/fill ../versions/versions.yaml registry.example:5000
```

`-plain-http` reaches the registry over HTTP. The e2e test fills its registry with the same code.

## Files

| Path | What it is |
|---|---|
| `helm/` | The helmfile of every release, our charts and the values |
| `versions/versions.yaml` | Every version and image digest a run uses, all of them this repo's packages |
| `ansible/` | The playbooks that install and upgrade k3s, and their roles |
| `ansible/k3s/` | The k3s config files of every node |
| `e2e/` | The e2e test, its client and the feature files |
| `e2e/images/` | The images this repo builds: the nodes, the VM of the KVM scenarios, the controller and the services outside k3s, with their config and the monitoring files |
| `e2e/cmd/` | The `fill`, `mirror` and `images` commands |

Licence: Apache License 2.0.
