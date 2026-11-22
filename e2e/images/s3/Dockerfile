# The store of the e2e test: versitygw, with its data in the container's own
# layer, so that docker commit keeps the buckets, users and versions. The
# run copies its TLS certificate in and sets it up.
FROM ghcr.io/versity/versitygw:v1.10.0@sha256:89cad13cc981986ee284cbafea4613f9533ce6fd5f37a2c96c4c4fb8adb3a316
RUN mkdir /data /iam /versions
