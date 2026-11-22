# The e2e image, built from the repository root for the machine's own
# architecture: docker-in-docker with the e2e runner, the client binary and
# its two-line Dockerfile, helm, kubectl, kubectl cnpg and k3s's script for
# new CAs. Each package, module and tool download of its RUN steps goes
# through retry.sh; the base images are not.
# TestScenario of scenario_test.go builds and runs it, with the build
# arguments below from versions/versions.yaml: the versions and sha256 of
# kubectl and kubectl cnpg, and k3s's script for new CAs.

FROM golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
WORKDIR /src
COPY e2e/retry.sh /usr/local/bin/retry
COPY e2e/go.mod e2e/go.sum e2e/
RUN cd e2e && retry go mod download
COPY e2e/ e2e/
RUN cd e2e && CGO_ENABLED=0 go build -trimpath -o /out/e2e . \
  && CGO_ENABLED=0 go build -trimpath -o /out/client ./client

FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f AS tools
COPY e2e/retry.sh /usr/local/bin/retry
WORKDIR /tools
ARG KUBECTL_VERSION
ARG KUBECTL_SHA256_AMD64
ARG KUBECTL_SHA256_ARM64
ARG CNPG_VERSION
ARG CNPG_SHA256_AMD64
ARG CNPG_SHA256_ARM64
RUN set -eu; \
  case $(uname -m) in \
    x86_64) arch=amd64 cnpgarch=x86_64 \
      helm=86584a54def73570558f66f5111cc53dfed56689637ae32c1201205d494f54fb \
      kubectl=$KUBECTL_SHA256_AMD64 \
      cnpg=$CNPG_SHA256_AMD64 ;; \
    aarch64) arch=arm64 cnpgarch=arm64 \
      helm=31c5794dd55c66a51e6b7d2e2ac7a114ae8b1de41ff1d9ba51748ac973b06a08 \
      kubectl=$KUBECTL_SHA256_ARM64 \
      cnpg=$CNPG_SHA256_ARM64 ;; \
    *) echo "no tools for $(uname -m)" >&2; exit 1 ;; \
  esac; \
  retry wget -q -O helm.tar.gz "https://get.helm.sh/helm-v4.3.0-linux-$arch.tar.gz"; \
  retry wget -q -O kubectl "https://dl.k8s.io/release/$KUBECTL_VERSION/bin/linux/$arch/kubectl"; \
  retry wget -q -O cnpg.tar.gz "https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v$CNPG_VERSION/kubectl-cnpg_${CNPG_VERSION}_linux_$cnpgarch.tar.gz"; \
  printf '%s  %s\n' "$helm" helm.tar.gz "$kubectl" kubectl "$cnpg" cnpg.tar.gz | sha256sum -c -; \
  mkdir out; \
  tar -xzf helm.tar.gz -C out --strip-components=1 "linux-$arch/helm"; \
  tar -xzf cnpg.tar.gz -C out kubectl-cnpg; \
  install -m 755 kubectl out/kubectl
# k3s's own script that makes new CAs, cross-signed by the old ones, for k3s
# certificate rotate-ca, of the k3s version the nodes run.
ARG ROTATE_CA_URL
ARG ROTATE_CA_SHA256
RUN set -eu; \
  retry wget -q -O rotate-default-ca-certs.sh "$ROTATE_CA_URL"; \
  echo "$ROTATE_CA_SHA256  rotate-default-ca-certs.sh" | sha256sum -c -; \
  mkdir k3s; \
  install -m 755 rotate-default-ca-certs.sh k3s/

FROM docker:29-dind@sha256:08a98015fce594af7c382883bea080ea206de74402a95da9dc52b6428c8cd121
# What k3s's script for new CAs needs, and what the nodes' data filesystems
# need: fallocate, mkfs.ext4, and util-linux's mount, umount and losetup.
COPY e2e/retry.sh /usr/local/bin/retry
RUN retry apk add --no-cache bash coreutils openssl \
  e2fsprogs losetup mount umount util-linux-misc
COPY --from=tools /tools/out/ /usr/local/bin/
COPY --from=tools /tools/k3s/ /opt/e2e/k3s/
COPY e2e/client/Dockerfile /opt/e2e/client/
COPY --from=build /out/client /opt/e2e/client/
COPY --from=build /out/e2e /usr/local/bin/e2e
# /results is there even for a run that stops before it writes, so the
# copy-out works. TestScenario copies the checkout's files in.
RUN mkdir /results
# The images of the nodes' data filesystems, on the runner's disk rather
# than in the container's own layer.
VOLUME ["/e2e-disks"]
ENTRYPOINT ["e2e"]
