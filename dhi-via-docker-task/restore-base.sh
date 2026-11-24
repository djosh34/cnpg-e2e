#!/usr/bin/env bash
# restore-base.sh <manifest-digest> <image-name>
#
# After a `FROM <image>@<manifest-digest>` build, puts that base image back
# together from the blobs the build downloaded into Docker's containerd
# content store, and loads it as <image-name>. The result is the upstream
# image byte for byte: its image ID is <manifest-digest>, and so is its
# digest once pushed. Needs the containerd image store, jq and sudo ctr.
set -euo pipefail

[ $# -eq 2 ] || { echo "usage: $0 <manifest-digest> <image-name>" >&2; exit 2; }
digest=$1 name=$2

# docker load keeps the name as written; Docker looks images up by the full
# name, so expand it as docker pull would: docker.io/library/x:latest.
first=${name%%/*}
if [[ $name != */* ]]; then name=docker.io/library/$name
elif [[ $first != *.* && $first != *:* && $first != localhost ]]; then name=docker.io/$name; fi
[[ ${name##*/} == *:* || $name == *@* ]] || name=$name:latest

docker info -f '{{.DriverStatus}}' | grep -q io.containerd.snapshotter \
  || { echo "Docker does not use the containerd image store; the build keeps no compressed layers" >&2; exit 1; }

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
mkdir -p "$dir/blobs/sha256"

get() {
  # shellcheck disable=SC2024 # the file is ours; only ctr needs root
  sudo ctr -n moby content get "$1" > "$dir/blobs/sha256/${1#sha256:}" \
    || { echo "$1 is not in the content store: build FROM ...@$digest first" >&2; exit 1; }
}

get "$digest"
manifest=$dir/blobs/sha256/${digest#sha256:}
case $(jq -r .mediaType "$manifest") in
  *manifest.v1+json | *manifest.v2+json) ;;
  *) echo "$digest is not a single-platform manifest: pin FROM to the manifest of one platform" >&2; exit 1 ;;
esac
for blob in $(jq -r '.config.digest, .layers[].digest' "$manifest"); do get "$blob"; done

printf '{"imageLayoutVersion":"1.0.0"}' > "$dir/oci-layout"
jq -cn --arg d "$digest" --argjson s "$(stat -c%s "$manifest")" --arg n "$name" \
  --arg mt "$(jq -r .mediaType "$manifest")" \
  '{schemaVersion: 2, manifests: [{mediaType: $mt, digest: $d, size: $s,
    annotations: {"org.opencontainers.image.ref.name": $n}}]}' > "$dir/index.json"

# docker load checks every blob against its digest.
tar -C "$dir" -cf "$dir/image.tar" oci-layout index.json blobs
docker load -i "$dir/image.tar"

id=$(docker image inspect -f '{{.Id}}' "$name")
[ "$id" = "$digest" ] || { echo "loaded $name as $id, not $digest" >&2; exit 1; }
echo "$name is $digest"
