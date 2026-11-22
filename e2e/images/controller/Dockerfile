# The controller of the e2e test, as the machine that runs Ansible in
# production: Ansible and an SSH client. The run copies the SSH key and the
# inventory in and runs Ansible in it.
FROM python:3.14.10-slim-trixie@sha256:9aaa474097d094040649f2162a627c0049b408f52138d980916788d80f04802f
RUN apt-get update \
  && apt-get install -y --no-install-recommends openssh-client \
  && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir ansible-core==2.22.0
CMD ["sleep", "infinity"]
