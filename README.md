# cnpg-e2e

A three-node k3s cluster with CloudNativePG, and an e2e runner that breaks it in scenarios: failover, switchover, recovery, a broken store, alerts and monitoring.

## Run a scenario

```bash
go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario switchover
```

- **Needs** an amd64 machine with Docker that allows privileged containers, Go, the host's `fs.inotify.max_user_instances` at 1024 or more (`bash e2e/inotify.sh` raises it) and the kernel module `br_netfilter` (`sudo modprobe br_netfilter`).
- **Results** go to `results/`. The test passes when the scenario passed.
- **Scenarios** are the tags in `e2e/features/`. `always.feature` runs after each one.

`go -C e2e test ./...` without `-scenario` runs the unit tests only and starts no container.

The scenario `airgap` runs on the machine's own Docker with only Go and Docker: the services outside k3s, three empty Rocky Linux 9 nodes with systemd and sshd on a network with no route to the internet, and a controller with Ansible. It checks that no node reaches the internet, that the controller reaches every node over SSH, and fills the registry with every image of the versions file.

On GitHub, the `e2e` workflow runs scenarios by hand, each in its own job, after it publishes the images of `e2e/images` that are new. The `ci` workflow runs lint and unit tests on every pull request. The `mirror` workflow copies the images and charts of the versions file from Docker Hardened Images into this repo's packages and uploads the versions file with their digests.

## Fill a registry

The nodes pull every image through a registry on the S3 store. To fill it, run a writable [Distribution](https://distribution.github.io/distribution/) on the same bucket, then copy every image of the versions file into it:

```bash
go -C e2e run ./cmd/fill ../versions/versions.yaml registry.example:5000
```

`-plain-http` reaches the registry over HTTP. The e2e test fills its registry with the same code.

## Files

| Path | What it is |
|---|---|
| `helm/` | The charts, their release list and values |
| `versions/versions.yaml` | Every version and image digest a run uses, all of them this repo's packages |
| `ansible/k3s/` | The k3s config files of every node |
| `e2e/` | The Go runner, its client and the feature files |
| `e2e/images/` | The images this repo builds: the nodes, the controller and the services outside k3s, with their config and the monitoring files |
| `e2e/cmd/` | The `fill`, `mirror` and `images` commands |

Licence: Apache License 2.0.
