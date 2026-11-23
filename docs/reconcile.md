# The reconcile

`helm/helmfile.yaml.gotmpl` holds every release. It uses the charts of `helm/charts/`, the upstream charts of the versions file and the values of `helm/values/`. The playbooks end with one `helmfile apply` from the first node. It installs or upgrades only the releases that changed. Each release starts once the releases it needs are ready, and a release that fails rolls back.

Every pod pulls its image through the registry on port 5000 of its node. That registry is a read-only [Distribution](https://distribution.github.io/distribution/) on the S3 bucket that [the fill](fill.md) writes to. On a new cluster the registry goes first, from the image archive in the k3s images directory, and then the prepull puts every image of the versions file on every node. On a running cluster the prepull goes first, so the new registry image is on every node before the old registry stops.

## Secrets

The role `helmfile` passes the company CA and the charts' secrets to helmfile as its environment. It reads them from these variables of the controller's environment. Helmfile writes its temporary values files to a tmpfs, and Ansible logs none of them.

| Variable | What it is |
|---|---|
| `COMPANY_CA` | The company CA, PEM. It signs the client certificates and the certificates of the ACME CA and the store. |
| `REGISTRY_S3_ACCESS_KEY`, `REGISTRY_S3_SECRET_KEY` | Keys that may read the registry's bucket |
| `REGISTRY_HTTP_SECRET` | `http.secret` of the writable registry of the fill |
| `BACKUP_S3_ACCESS_KEY`, `BACKUP_S3_SECRET_KEY` | Keys of the backups' bucket |
| `PG_SERVER_EAB_KID`, `PG_SERVER_EAB_HMAC` | An EAB key of the ACME CA for the database's server certificate |
| `PG_REPLICATION_EAB_KID`, `PG_REPLICATION_EAB_HMAC` | One for its replication certificate |
| `OTEL_CLIENT_EAB_KID`, `OTEL_CLIENT_EAB_HMAC` | One for the OTel collectors' client certificate |
| `PG_RESTORE_SERVER_EAB_KID`, `PG_RESTORE_SERVER_EAB_HMAC`, `PG_RESTORE_REPLICATION_EAB_KID`, `PG_RESTORE_REPLICATION_EAB_HMAC` | Only with `pg_restore=true`. One each for the server and replication certificates of `pg-restore`. |

## pg-restore

`pg_restore=true` adds the release `pg-restore`. It is a second database cluster that restores the backups and WAL of `pg` from the store, with no LoadBalancer of its own. A later run without the variable leaves `pg-restore` as it is.
