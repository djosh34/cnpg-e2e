#!/usr/bin/env bash
#
# restore-base.sh - get the base image of a FROM-only build back, byte for byte.
#
# Usage:
#   restore-base.sh <manifest-digest> <image-name>
#
# Example:
#   restore-base.sh \
#     sha256:c83caf23643786d88605167e77cdb95bc97c7a2eb213fe6c884927026afae5d0 \
#     myregistry.example.com/dhi/opentelemetry-collector:0.165.0
#
# How it works:
#   A `docker build` of a Dockerfile with only `FROM <image>@<manifest-digest>`
#   downloads that image's manifest, config and layers into Docker's
#   containerd content store. The build's own result is a new image, with the
#   labels the Docker@2 task adds, but the downloaded files stay in the store.
#
#   This script copies those files out, puts them in an OCI image layout and
#   loads that with `docker load`. The loaded image is the upstream image
#   itself, so its ID, and its digest once pushed, is <manifest-digest>.
#
# Needs:
#   - Docker with the containerd image store
#   - jq
#   - ctr, run as root or through sudo
#   - CONTAINERD_ADDRESS, only when containerd's socket is not the default
#     /run/containerd/containerd.sock (in Docker-in-Docker it is
#     /var/run/docker/containerd/containerd.sock)

set -euo pipefail


main() {
  if [ $# -ne 2 ]; then
    echo "usage: $0 <manifest-digest> <image-name>" >&2
    exit 2
  fi

  digest=$1
  name=$(full_image_name "$2")

  check_containerd_store

  # Work in a temporary directory that is removed again however we exit.
  dir=$(mktemp -d)
  trap 'rm -rf "$dir"' EXIT
  mkdir -p "$dir/blobs/sha256"

  copy_image_from_content_store
  write_oci_layout
  load_image
  check_loaded_image
}


# Docker looks images up by their full name, such as
# docker.io/library/debian:latest, but `docker load` keeps a name exactly as
# written. So write the name out in full, as `docker pull` would:
#   debian              -> docker.io/library/debian:latest
#   grafana/grafana:12  -> docker.io/grafana/grafana:12
#   ghcr.io/x/y:1       -> ghcr.io/x/y:1 (unchanged)
full_image_name() {
  local name=$1
  local first_part=${name%%/*}

  if [[ $name != */* ]]; then
    # A bare name is an official Docker Hub image.
    name=docker.io/library/$name
  elif [[ $first_part != *.* && $first_part != *:* && $first_part != localhost ]]; then
    # The first part is not a registry host, so it is a Docker Hub user.
    name=docker.io/$name
  fi

  # No tag means latest.
  if [[ ${name##*/} != *:* && $name != *@* ]]; then
    name=$name:latest
  fi

  echo "$name"
}


# Docker's default image store unpacks layers and throws the downloaded
# files away; only the containerd image store keeps them.
check_containerd_store() {
  if ! docker info --format '{{.DriverStatus}}' | grep -q io.containerd.snapshotter; then
    fail "Docker does not use the containerd image store, so the build kept no layers to restore"
  fi
}


# Copy the manifest, then the config and every layer it lists.
copy_image_from_content_store() {
  copy_blob "$digest"
  manifest=$dir/blobs/sha256/${digest#sha256:}

  # One manifest describes one platform. A multi-platform index points at
  # several manifests, and the build only downloaded the one it used.
  case $(jq -r .mediaType "$manifest") in
    application/vnd.oci.image.manifest.v1+json) ;;
    application/vnd.docker.distribution.manifest.v2+json) ;;
    *) fail "$digest is not a single-platform manifest: pin FROM to the manifest of one platform" ;;
  esac

  local blob
  for blob in $(jq -r '.config.digest, .layers[].digest' "$manifest"); do
    copy_blob "$blob"
  done
}


# Copy one file from the content store, named by its digest as in an OCI layout.
copy_blob() {
  local blob=$1

  # shellcheck disable=SC2024 # only ctr needs root; the file stays ours
  if ! as_root ctr --address "${CONTAINERD_ADDRESS:-/run/containerd/containerd.sock}" \
      --namespace moby content get "$blob" > "$dir/blobs/sha256/${blob#sha256:}"; then
    fail "$blob is not in the content store: first build a Dockerfile with only FROM ...@$digest"
  fi
}


# An OCI image layout is a directory with oci-layout, index.json and blobs/.
# index.json points at our manifest and names the image, which is the name
# `docker load` gives it.
write_oci_layout() {
  printf '{"imageLayoutVersion":"1.0.0"}' > "$dir/oci-layout"

  jq --null-input --compact-output \
    --arg digest "$digest" \
    --arg name "$name" \
    --arg media_type "$(jq -r .mediaType "$manifest")" \
    --argjson size "$(stat -c %s "$manifest")" \
    '{
      schemaVersion: 2,
      manifests: [{
        mediaType: $media_type,
        digest: $digest,
        size: $size,
        annotations: {"org.opencontainers.image.ref.name": $name}
      }]
    }' > "$dir/index.json"
}


# docker load checks every file against its digest while it loads.
load_image() {
  tar --directory "$dir" --create --file "$dir/image.tar" oci-layout index.json blobs
  docker load --input "$dir/image.tar"
}


# An image's ID in the containerd store is the digest of its manifest, so the
# ID must be the digest we started from.
check_loaded_image() {
  local id
  id=$(docker image inspect --format '{{.Id}}' "$name")

  if [ "$id" != "$digest" ]; then
    fail "loaded $name as $id instead of $digest"
  fi

  echo "$name is $digest"
}


as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    sudo "$@"
  fi
}


fail() {
  echo "restore-base.sh: $1" >&2
  exit 1
}


main "$@"
