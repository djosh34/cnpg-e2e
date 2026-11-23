# The company CA of the e2e test: OpenBao, whose PKI serves ACME. The run
# copies its TLS certificate in, then initializes, unseals and sets it up.
FROM openbao/openbao:2.7.2@sha256:a78eb12a9fc7a3ebb4bec1724e2bdd6a28e33394820641d061166919a3f1866b
COPY bao.hcl /bao/bao.hcl
CMD ["server", "-config=/bao/bao.hcl"]
