#!/bin/sh
# Starts the VM and runs it in the foreground, with its serial console on
# stdout. The VM takes this container's place on the network: eth0 and the
# VM's tap device join one bridge, and the VM's cloud-init seed in /seed
# gives it this container's address. Its disk is a layer in /disk over the
# cloud image, which takes the writes.
set -eu
ip link add br0 type bridge
ip tuntap add tap0 mode tap
ip address flush dev eth0
ip link set eth0 master br0
ip link set tap0 master br0
ip link set br0 up
ip link set tap0 up
genisoimage -quiet -output /seed.iso -volid cidata -joliet -rock /seed/user-data /seed/meta-data /seed/network-config
qemu-img create -q -f qcow2 -F qcow2 -b /rocky.qcow2 /disk/disk.qcow2 40G
exec qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp 2 -m 4G \
  -nodefaults -display none -serial stdio \
  -drive file=/disk/disk.qcow2,if=virtio,discard=unmap \
  -drive file=/seed.iso,format=raw,if=virtio,readonly=on \
  -netdev tap,id=net,ifname=tap0,script=no,downscript=no \
  -device virtio-net-pci,netdev=net,mac="$MAC"
