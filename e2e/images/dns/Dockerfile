# The DNS of the e2e test: CoreDNS with the names of the hosts file the run
# copies in. Every other name goes to Docker's resolver.
FROM coredns/coredns:1.15.0@sha256:3429e3bbffb1e839470c4a3541d4d78aca84a1394c75a539d0200c58c63196ac
COPY Corefile /etc/coredns/Corefile
CMD ["-conf", "/etc/coredns/Corefile"]
