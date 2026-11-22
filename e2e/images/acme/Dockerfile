# The company CA of the e2e test: OpenBao, whose PKI serves ACME. The run
# copies its TLS certificate in, then initializes, unseals and sets it up.
FROM openbao/openbao:2.7.2@sha256:5046911075e137e154ce49ab051502f4ec7cf367d484ae4a4dce665d9b90561f
COPY bao.hcl /bao/bao.hcl
CMD ["server", "-config=/bao/bao.hcl"]
