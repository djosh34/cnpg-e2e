# A node of the KVM scenarios: a Rocky Linux 9 VM, from its cloud image,
# which boots with SELinux enforcing. vm.sh starts it.
ARG BASE
FROM ${BASE}
ARG DISK_URL
ARG DISK_SHA256
RUN apt-get update \
  && apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils genisoimage iproute2 \
  && rm -rf /var/lib/apt/lists/*
ADD --checksum=sha256:${DISK_SHA256} ${DISK_URL} /rocky.qcow2
COPY --chmod=755 vm.sh /usr/local/bin/vm
CMD ["vm"]
