# The controller of the e2e test, as the machine that runs Ansible in
# production: Ansible, an SSH client, and regctl, which fetches the charts
# and the registry's image by digest. The run copies the SSH key and the
# inventory in and runs Ansible in it.
FROM python:3.14.10-slim-trixie@sha256:ac25e5afbfdb4a94ff99cc24b0345719210133144481480bcba6085efa36a341
RUN apt-get update \
  && apt-get install -y --no-install-recommends openssh-client \
  && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir ansible-core==2.22.0
ADD --checksum=sha256:3737fbbdd791e04e7a831640b681f3397a376498e80e2ad9b1b2d27296f239bc --chmod=755 \
  https://github.com/regclient/regclient/releases/download/v0.11.7/regctl-linux-amd64 /usr/local/bin/regctl
CMD ["sleep", "infinity"]
