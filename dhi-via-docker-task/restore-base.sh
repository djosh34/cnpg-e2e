#!/usr/bin/env bash
#
# restore-base.sh <manifest-digest> <image-name>
#
# After a `docker build` of a Dockerfile with only `FROM <image>@<manifest-digest>`,
# Docker's containerd content store still holds that image's manifest, config
# and layers. This copies them out and loads them back as <image-name>: the
# upstream image byte for byte, so its digest is <manifest-digest>.
#
# Needs Docker with the containerd image store, jq, and ctr (run as root or
# through sudo). Set CONTAINERD_ADDRESS when containerd's socket is not
# /run/containerd/containerd.sock.

set -euo pipefail

digest=${1:?usage: restore-base.sh <manifest-digest> <image-name>}
name=${2:?usage: restore-base.sh <manifest-digest> <image-name>}

fail() { echo "restore-base.sh: $*" >&2; exit 1; }

# Docker's default image store throws the downloaded files away.
docker info --format '{{.DriverStatus}}' | grep -q io.containerd.snapshotter ||
  fail "Docker does not use the containerd image store"

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
mkdir -p "$dir/blobs/sha256"

# Copy one file out of the content store, into blobs/ of an OCI image layout.
copy() {
  local sudo=sudo
  [ "$(id -u)" -ne 0 ] || sudo=

  $sudo ctr --address "${CONTAINERD_ADDRESS:-/run/containerd/containerd.sock}" \
    --namespace moby content get "$1" > "$dir/blobs/sha256/${1#sha256:}" ||
    fail "$1 is not in the content store: build FROM ...@$digest first"
}

# The manifest, then the config and layers it lists.
copy "$digest"
manifest=$dir/blobs/sha256/${digest#sha256:}

jq -e .config "$manifest" > /dev/null ||
  fail "$digest is a multi-platform index: pin FROM to one platform's manifest"

for blob in $(jq -r '.config.digest, .layers[].digest' "$manifest"); do
  copy "$blob"
done

# The rest of the OCI image layout: oci-layout, and index.json pointing at the manifest.
echo '{"imageLayoutVersion":"1.0.0"}' > "$dir/oci-layout"

jq -nc --arg digest "$digest" \
  --arg type "$(jq -r .mediaType "$manifest")" \
  --argjson size "$(stat -c %s "$manifest")" \
  '{schemaVersion: 2, manifests: [{mediaType: $type, digest: $digest, size: $size}]}' \
  > "$dir/index.json"

# docker load checks every file against its digest; the image's ID is the
# manifest's digest, so that is what we tag.
tar -C "$dir" -c oci-layout index.json blobs | docker load
docker tag "$digest" "$name"
echo "$name is $digest"
