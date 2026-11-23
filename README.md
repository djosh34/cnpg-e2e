# cnpg-e2e

A three-node k3s cluster with CloudNativePG, and an e2e runner that breaks it in scenarios: failover, switchover, recovery, a broken store, alerts and monitoring.

## Run a scenario

```bash
go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario switchover
```

- **Needs** an amd64 machine with Go and a Docker that allows privileged containers. A run first loads the kernel module `br_netfilter` on the machine and raises its `fs.inotify.max_user_instances` to 1024, from a privileged container.
- **Results** go to `results/`. The test passes when the scenario passed.
- **Scenarios** are the tags in `e2e/features/`. `always.feature` runs after each one.

`go -C e2e test ./...` without `-scenario` runs the unit tests only and starts no container.

The scenario `airgap` runs on the machine's own Docker with only Go and Docker: the services outside k3s, three empty Rocky Linux 9 nodes with systemd and sshd on a network with no route to the internet, and a controller with Ansible. It checks that no node reaches the internet, that the controller reaches every node over SSH, and fills the registry with every image of the versions file.

The scenario `install` starts the same machines with empty nodes, fills the registry and runs `install.yml`: k3s, then every chart. It checks that the database cluster is healthy, has its first backup and sends the metrics of every source to Prometheus, then that `upgrade.yml` on the running cluster changes nothing.

The scenario `restore` starts the same machines from the cache: a clean k3s cluster that `install.yml` installed without its helmfile step, with no charts, and the store and company CA next to it. It checks that every node turns Ready, then fills the registry, runs `upgrade.yml` and checks the cluster as `install` does. The cache is one image per container in this repo's packages, tagged by a hash of what makes it: the node, store and CA images, the k3s and node tools of the versions file, `ansible/` but its role `helmfile`, and the Go code that sets the machines up. A run that finds no cache builds it on the machine. The scenario `cache` builds it when the packages do not have it, and with `-push` pushes it, which only CI does.

On GitHub, the `e2e` workflow runs scenarios by hand, each in its own job, after it publishes the images of `e2e/images` that are new and the cache when it is new. The `ci` workflow runs lint and unit tests on every pull request. The `mirror` workflow copies the images and charts of the versions file from Docker Hardened Images into this repo's packages and uploads the versions file with their digests.

## Install k3s with Ansible

`ansible/install.yml` installs k3s on the hosts of the inventory group `nodes`, each a server with embedded etcd, one at a time. `ansible/upgrade.yml` brings a running cluster to the files of the checkout and restarts k3s only on a node whose binary or config changed. Both share the role `k3s`, which writes `config.yaml` (the settings of `ansible/k3s/config.yaml` and the node's own), `registries.yaml` and the manifests of `ansible/k3s/manifests/`, copies the airgap files and the registry's image, and runs one CoreDNS pod per node. Both then install Helm, its diff plugin and helmfile, and last run one `helmfile apply` of every chart from the first node (below). The nodes need no internet: the controller downloads every file of the versions file, and fetches the charts and the registry's image with [regctl](https://regclient.org/), each by its digest; Ansible copies them to the nodes.

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

`dns_server` sets the DNS server of k3s and its pods, where the nodes' own `/etc/resolv.conf` does not do. `--skip-tags helmfile` installs k3s alone.

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
| `e2e/` | The Go runner, its client and the feature files |
| `e2e/images/` | The images this repo builds: the nodes, the controller and the services outside k3s, with their config and the monitoring files |
| `e2e/cmd/` | The `fill`, `mirror` and `images` commands |

Licence: Apache License 2.0.
