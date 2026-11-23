#!/bin/sh
# Gives eth0 a default route through the first address of its network, the
# Docker network's gateway, as a machine of production has a default route.
# k3s needs one to pick its interface and to reach the ClusterIPs. On the e2e
# test's internal network the gateway forwards nothing out, so the node
# still has no internet.
set -eu
address=$(ip -4 -o address show dev eth0 | awk '{print $4}')
gateway=$(python3 -c 'import ipaddress, sys; print(next(ipaddress.ip_interface(sys.argv[1]).network.hosts()))' "$address")
ip route replace default via "$gateway" dev eth0
