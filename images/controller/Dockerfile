# The controller, as the machine that runs Ansible in production. The e2e
# also runs its client in it.
ARG BASE
FROM ${BASE}
ARG ANSIBLE_CORE
RUN apt-get update \
  && apt-get install -y --no-install-recommends openssh-client \
  && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir "ansible-core==${ANSIBLE_CORE}"
CMD ["sleep", "infinity"]
