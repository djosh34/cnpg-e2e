# A node: Rocky Linux 9 with systemd and sshd, as a machine of production.
# k3s-killall.sh needs ps, k3s's CA script openssl, Ansible's unarchive tar.
ARG BASE
FROM ${BASE}
RUN dnf install -y --setopt=install_weak_deps=False systemd openssh-server iproute procps-ng openssl tar \
  && dnf clean all
COPY --chmod=755 default-route.sh /usr/local/sbin/default-route
COPY default-route.service /etc/systemd/system/
# On hosts with AppArmor, its unix-chkpwd profile cannot read Rocky's
# /etc/shadow of mode 0000, and sshd's PAM check fails; root may read 0400.
RUN systemctl enable sshd.service default-route.service \
  && chmod 0400 /etc/shadow
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
