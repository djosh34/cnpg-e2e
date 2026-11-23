# A node of the e2e test's KVM scenarios: a Rocky Linux 9.9 VM, from its
# cloud image, under QEMU with KVM. The image boots with SELinux enforcing.
# The run starts the container privileged, for /dev/kvm and its network,
# with the VM's cloud-init seed in /seed, a volume at /disk and the VM's MAC
# address in MAC; vm.sh starts the VM.
FROM debian:trixie-20261104-slim@sha256:7378260c0a098d27ac206225db5e7d92b8c7c84ea7afccc86eeb513756bbd392
RUN apt-get update \
  && apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils genisoimage iproute2 \
  && rm -rf /var/lib/apt/lists/*
ADD --checksum=sha256:4f88f4209de50813d8054ab97309e7169a580206a0c4ea04f375d36c4846d50e \
  https://dl.rockylinux.org/pub/rocky/9.9/images/x86_64/Rocky-9-GenericCloud-Base-9.9-20261112.0.x86_64.qcow2 /rocky.qcow2
COPY --chmod=755 vm.sh /usr/local/bin/vm
CMD ["vm"]
