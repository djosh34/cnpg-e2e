# DHI through the Docker@2 task

On our Azure DevOps agents, a Docker@2 `build` can fetch `dhi.io` images while
`docker pull` cannot. A one-line `FROM` build downloads the image's own
manifest, config and layers into Docker's containerd content store.
[`restore-base.sh`](restore-base.sh) loads that image back from there. The
result is upstream's image byte for byte, with **the same digest as upstream**.
Docker@2's labels stay on the throwaway build image.

## Pipeline

`dhi/Dockerfile`, pinned to the **linux/amd64 manifest** digest, not the
multi-platform index digest
([`versions/versions.yaml`](../versions/versions.yaml) pins exactly these):

```dockerfile
FROM dhi.io/opentelemetry-collector@sha256:c83caf23643786d88605167e77cdb95bc97c7a2eb213fe6c884927026afae5d0
```

```yaml
# 1. The containerd image store: the default store keeps no compressed layers.
- script: |
    f=/etc/docker/daemon.json
    [ -s $f ] || echo '{}' | sudo tee $f >/dev/null
    jq '.features["containerd-snapshotter"]=true' $f | sudo tee $f.new >/dev/null
    sudo mv $f.new $f && sudo systemctl restart docker

# 2. The "pull".
- task: Docker@2
  inputs:
    command: build
    Dockerfile: dhi/Dockerfile
    repository: throwaway
    arguments: --platform linux/amd64

# 3. The upstream image, as myregistry.example.com/dhi/opentelemetry-collector:0.165.0.
- script: >
    dhi-via-docker-task/restore-base.sh
    sha256:c83caf23643786d88605167e77cdb95bc97c7a2eb213fe6c884927026afae5d0
    myregistry.example.com/dhi/opentelemetry-collector:0.165.0

# 4. Push it; the pushed digest is the one in FROM.
- task: Docker@2
  inputs:
    command: push
    containerRegistry: myRegistryConnection
    repository: dhi/opentelemetry-collector
    tags: 0.165.0
```

## Notes

- **The script refuses rather than produce a different image.** It exits with
  an error when the store is not containerd, when the digest was not fetched,
  or when the digest is an index; `docker load` checks every file's digest.
- **Run step 3 right after step 2.** No image holds the base's blobs, so Docker
  may clean them up later.
- **Requirements:** `jq`, and `ctr` with `sudo`. Microsoft-hosted Ubuntu agents
  should have both; check with `which ctr`.
- **Tested** on all 22 images in `versions/versions.yaml`: every pushed digest
  equals its pin, with Docker 29 and with Docker 28.0.4 (the hosted agents'
  version). Not yet on an agent: check first that step 2 still fetches
  `dhi.io` after the restart in step 1.
