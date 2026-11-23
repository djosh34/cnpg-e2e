# The e2e runner

The e2e test in `e2e/` starts a three-node cluster in Docker, installs it with [the playbooks](playbooks.md) and breaks it in scenarios.

```bash
go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario install
```

It needs an amd64 machine with Go and a Docker that allows privileged containers. A run first loads the kernel module `br_netfilter` and raises `fs.inotify.max_user_instances` to 1024, from a privileged container. It writes its files to `results/<scenario>/`. The test passes when the scenario passed. `go -C e2e test ./...` without `-scenario` runs the unit tests and starts no container.

## The machines

Every scenario starts these machines on the machine's own Docker:

- the services outside k3s, from Go with testcontainers-go: DNS, the S3 store, the company ACME CA, the OTel receiver, Prometheus, Alertmanager, Grafana and the writable registry of [the fill](fill.md)
- three Rocky Linux 9 nodes with systemd and sshd, on a Docker network with no route to the internet, or three VMs in the KVM scenarios
- a controller container that runs Ansible

The scenarios of `e2e/features/` also start the client, the application outside the cluster, in a container next to the nodes.

The images of `e2e/images/` are this repo's packages, tagged by a hash of their files. A run builds an image when its tag is missing.

## Scenarios

| Scenario | What it does |
|---|---|
| `airgap` | Checks that no node reaches the internet, that the controller reaches every node over SSH, and fills the registry. |
| `install` | Fills the registry and runs `install.yml` on empty nodes. It checks that the database cluster is healthy, has its first backup and sends the metrics of every source to Prometheus. Then it checks that `upgrade.yml` changes nothing. |
| `k3s-upgrade` | Runs `install.yml` with `k3s_previous=true`, then `upgrade.yml` to the k3s of the versions file. It checks that every node runs that k3s, that every container runs an image of the versions file, and the cluster as `install` does. |
| `install-kvm`, `k3s-upgrade-kvm` | The same on three Rocky Linux 9 VMs under QEMU and KVM with SELinux enforcing and `selinux=true`. They also check that k3s runs in the SELinux domain `container_runtime_t`. Only these two need `/dev/kvm`. |
| `cache` | Builds the cache when the packages do not have it. With `-push` it pushes it, which only CI does. |
| `restore` | Starts from the cache, fills the registry, runs `upgrade.yml` and checks the cluster as `install` does. |
| The tags of `e2e/features/` | Each starts as `restore` does, runs its own lines, then those of `always.feature`. |

The cache is one image per container: a clean k3s cluster that `install.yml --skip-tags helmfile` installed, with no charts, and the store and company CA next to it. Its tag is a hash of the node, store and CA images, the k3s and node tools of the versions file, `ansible/` without its role `helmfile`, and the Go code that sets the machines up. A run that finds no cache builds it.

`always.feature` checks that the cluster is healthy, that no read or write gap is over the limit the feature file sets, that no acknowledged write is lost, that only the alerts the scenario names fired, that the dashboard has data, and that every image on the nodes is one the versions file pins. The test runs `kubectl` and `kubectl cnpg` of the versions file, which it downloads and checks against their sha256.

`data-disk-full` fills a node's data disk, so there every node gets a tmpfs of 1 GiB for its database volumes. Every other scenario keeps them on a Docker volume.

`image-upgrade` first deploys an earlier versions file. That is the `versions.yaml` of the latest release, which on GitHub is never a nightly's prerelease. With `-nightly`, as the nightly runs it, it is the newest release, so last night's. When it has none, or it equals the checkout's file apart from k3s and comments, it is the newest version of `versions/versions.yaml` in the history of `origin/main` that differs in more than that, so the scenario needs a clone with that history. Without `-nightly` a nightly's file is never the start, because a pull request's file can be older than last night's. It keeps the checkout's k3s, which takes no downgrade. Then `upgrade.yml` applies the checkout's versions file. It checks that every running container moves to an image of that file and that releases whose manifests did not change keep their revision. Its image check accepts the images of both files.

`ca-rotation` runs `rotate-ca.yml` and checks that every node serves and uses certificates of the new CAs.

## On GitHub

The `e2e` workflow runs each scenario in its own job, in parallel. It runs every scenario on each pull request. By hand, it runs the scenarios its input `scenarios` names, or every scenario when the input is empty. Its first job publishes the images of `e2e/images/` that are new. The cache job publishes the cache when it is new, and only the scenarios that start from the cache wait for it. A pull request from a fork may not push packages, so there each scenario job builds what the packages lack. Each scenario job uploads `results/`.

The `ci` workflow runs lint and unit tests on every pull request. The `mirror` workflow runs `cmd/mirror` by hand ([the fill](fill.md)).

## The nightly

The `e2e` workflow also runs every night at 03:23 UTC, and by hand with its input `nightly` on. The nightly does this:

1. It logs in to Docker Hardened Images with the repository secrets `DHI_USERNAME` and `DHI_TOKEN`.
2. It runs `cmd/mirror -latest`, which copies the newest images and charts into this repo's packages and writes a new versions file. That file keeps `k3s.previous` and every download.
3. It runs every scenario on that versions file.
4. It publishes a prerelease `nightly-<run id>.<attempt>` with two files, also when scenarios failed: `versions.yaml` and `report.yaml`.

`report.yaml` names the commit, the run and the result of each scenario:

```yaml
commit: 0123abcd...
run: https://github.com/djosh34/cnpg-e2e/actions/runs/1
scenarios:
  - name: airgap
    result: passed
    job: https://github.com/djosh34/cnpg-e2e/actions/runs/1/job/2
```

The result is `passed`, `failed`, or `skipped` when a job before the scenario failed. The nightly pushes no commit.
