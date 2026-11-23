# The fill and the versions file

## The versions file

`versions/versions.yaml` pins every version a cluster runs. It names k3s and its airgap files, the SELinux packages, Helm, its diff plugin and helmfile, each by URL and sha256. It names every chart and image by tag and digest. The charts and images are this repo's packages under `ghcr.io/djosh34/cnpg-e2e`. They are copies of [Docker Hardened Images](https://docs.docker.com/dhi/) and of the upstream barman-cloud chart, which Docker Hardened Images does not have. Each image digest is the digest of the multi-arch index.

Each nightly release has the versions file it ran on ([the runner](runner.md#the-nightly)). A pull request can swap one in.

## Fill a registry

The nodes have no internet and pull every image through a registry on the S3 store. To fill it, run a writable [Distribution](https://distribution.github.io/distribution/) on the same bucket outside the airgap, then copy every image of the versions file into it:

```bash
go -C e2e run ./cmd/fill ../versions/versions.yaml registry.example:5000
```

`-plain-http` reaches the registry over HTTP. The fill copies each image by its digest, every platform of it, so the digests stay the same. The e2e test fills its registry with the same code.

## Copy the images and charts into this repo's packages

`cmd/mirror` copies every chart and image of the versions file from its source into this repo's packages, with its signatures and attestations. Then it writes the digest it copied into the file. It needs `docker login` to `dhi.io` and to `ghcr.io` with the right to push this repo's packages. Only this repo's workflows push them.

```bash
go -C e2e run ./cmd/mirror ../versions/versions.yaml
go -C e2e run ./cmd/mirror -latest ../versions/versions.yaml
```

Without `-latest` it copies the tags the file names. With `-latest` it copies the newest tag of the same kind at the source and writes that tag too. The same kind means the same first version number, as many version numbers and the same suffix. For `18.7-debian13` that may be `18.8-debian13`, never `19.1-debian13` or `18.8-alpine3.24`. A new first number, such as a PostgreSQL major upgrade, stays a change by hand. `-latest` leaves k3s, the tools and every other download as they are.

The `mirror` workflow runs it by hand without `-latest` and uploads the new versions file. The nightly runs it with `-latest`.
