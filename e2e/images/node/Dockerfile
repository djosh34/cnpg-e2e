# A node of the e2e test: Rocky Linux 9 with systemd and sshd, as a machine
# of production. The run starts it privileged, with tmpfs on /run, /run/lock
# and /tmp, and copies root's authorized_keys in. Ansible needs python3,
# which the base image has.
FROM quay.io/rockylinux/rockylinux:9.9@sha256:bb5755f96bec05ff26165e779ec843d7167544be9e50770508f307019a6d05c4
RUN dnf install -y --setopt=install_weak_deps=False systemd openssh-server iproute \
  && dnf clean all
COPY --chmod=755 default-route.sh /usr/local/sbin/default-route
COPY default-route.service /etc/systemd/system/
RUN systemctl enable sshd.service default-route.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
