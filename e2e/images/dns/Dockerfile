# The DNS of the e2e test: CoreDNS with the names of the hosts file the run
# copies in. Every other name goes to Docker's resolver.
FROM coredns/coredns:1.15.0@sha256:0a9d8c8ddf34347eb59aad9a8784f0710de88d6d112726d11d271f9bc6daca26
COPY Corefile /etc/coredns/Corefile
CMD ["-conf", "/etc/coredns/Corefile"]
