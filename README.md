# cnpg-e2e

A three-node k3s cluster with CloudNativePG, installed airgapped, and an e2e test that breaks it in scenarios. The scenarios cover failover, switchover, recovery, a broken store, alerts, monitoring, upgrades of the images and of k3s, and a rotation of k3s's CAs.

Production and the e2e test install the cluster with the same commands. An Ansible playbook installs or upgrades k3s on the nodes and ends with one `helmfile apply` of every chart. The test writes no namespaces, Secrets, patches or k3s files of its own.

## Install

1. Fill the registry on the S3 store with the images of `versions/versions.yaml` ([the fill](docs/fill.md)).
2. Set the secrets of the charts in the controller's environment ([the reconcile](docs/reconcile.md#secrets)).
3. Run the install playbook ([the playbooks](docs/playbooks.md)):

```bash
cd ansible && ansible-playbook -i inventory.ini install.yml
```

`upgrade.yml` takes a running cluster to a new checkout the same way.

## Test

```bash
go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario install
```

This needs an amd64 machine with Go and a Docker that allows privileged containers. [The runner](docs/runner.md) lists every scenario.

## Releases

A nightly copies the newest Docker Hardened Images and charts into this repo's packages, runs every scenario on them and publishes a release of two files: the versions file it ran on and `report.yaml`, the result of each scenario ([the nightly](docs/runner.md#the-nightly)). A pull request can swap that versions file in.

## Files

| Path | What it is |
|---|---|
| `ansible/` | The playbooks that install and upgrade k3s, and their roles |
| `ansible/k3s/` | The k3s config files of every node |
| `helm/` | The helmfile of every release, our charts and the values |
| `versions/versions.yaml` | Every version and digest a cluster runs |
| `e2e/` | The e2e test, its client and the feature files |
| `e2e/images/` | The images this repo builds: the nodes, the VM of the KVM scenarios, the controller and the services outside k3s |
| `e2e/cmd/` | The `fill`, `mirror` and `images` commands |
| `docs/` | [The playbooks](docs/playbooks.md), [the reconcile](docs/reconcile.md), [the fill](docs/fill.md) and [the runner](docs/runner.md) |

Licence: Apache License 2.0.
