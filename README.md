# cnpg-e2e

A three-node k3s cluster with CloudNativePG, installed airgapped by the same commands in production and in its e2e, which breaks it in scenarios.

Install from a checkout, on a machine with internet and SSH to the nodes:

```bash
go run ./cmd/fill <registry host:port>  # files/, the charts, and every image into the nodes' registry
cd ansible && ansible-playbook -i <inventory> install.yml
```

The inventory sets `selinux=true` or `false` for the group `nodes`, and the environment holds the charts' secrets (`ansible/roles/helmfile`). The same playbook upgrades a running cluster.

Test on an amd64 machine with Go and Docker: `go run ./e2e list`, then `go run ./e2e <scenario>`.

A nightly copies the newest Docker Hardened Images into this repo's packages (`go run ./cmd/mirror -latest`), runs every scenario on them, and releases that `versions/versions.yaml` with `report.yaml`.

Licence: Apache License 2.0.
