# A node of the e2e test: Rocky Linux 9 with systemd and sshd, as a machine
# of production. The run starts it privileged, with tmpfs on /run, /run/lock
# and /tmp, and copies root's authorized_keys in. Ansible needs python3,
# which the base image has.
FROM quay.io/rockylinux/rockylinux:9.9@sha256:bb5755f96bec05ff26165e779ec843d7167544be9e50770508f307019a6d05c4
RUN dnf install -y --setopt=install_weak_deps=False systemd openssh-server iproute \
  && dnf clean all
COPY --chmod=755 default-route.sh /usr/local/sbin/default-route
COPY default-route.service /etc/systemd/system/
# sshd's PAM account check runs unix_chkpwd. On hosts with AppArmor, such as
# Ubuntu, AppArmor's unix-chkpwd profile attaches to the node's copy too and
# grants no capability to read Rocky's /etc/shadow of mode 0000: root may
# read it as its owner at 0400.
RUN systemctl enable sshd.service default-route.service \
  && chmod 0400 /etc/shadow
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
