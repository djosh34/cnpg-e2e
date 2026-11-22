# cnpg-e2e

A three-node k3s cluster with CloudNativePG, and an e2e runner that breaks it in scenarios: failover, switchover, recovery, a broken store, alerts and monitoring.

## Run a scenario

```bash
go -C e2e test -run '^TestScenario$' -count=1 -timeout 90m -v . -scenario switchover
```

- **Needs** Docker that allows privileged containers, Go, the host's `fs.inotify.max_user_instances` at 1024 or more (`bash e2e/inotify.sh` raises it) and the kernel module `br_netfilter` (`sudo modprobe br_netfilter`).
- **Results** go to `results/`. The test passes when the scenario passed.
- **Scenarios** are the tags in `e2e/features/`. `always.feature` runs after each one.

`go -C e2e test ./...` without `-scenario` runs the unit tests only and starts no container.

On GitHub, the `e2e` workflow runs scenarios by hand, each in its own job. The `ci` workflow runs lint and unit tests on every pull request.

## Files

| Path | What it is |
|---|---|
| `helm/` | The charts, their release list and values |
| `versions/versions.yaml` | Every version and image digest a run uses |
| `ansible/k3s/` | The k3s config files of every node |
| `e2e/` | The Go runner, its client, the feature files and the monitoring files |

Licence: Apache License 2.0.
