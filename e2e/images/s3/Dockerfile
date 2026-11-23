# The store of the e2e test: versitygw, with its data in the container's own
# layer, so that docker commit keeps the buckets, users and versions. The
# run copies its TLS certificate in and sets it up.
FROM ghcr.io/versity/versitygw:v1.10.0@sha256:2df780de109d16491cb99c25f4c0bc9085b286e77b5b47b56121b340094e70bb
RUN mkdir /data /iam /versions
