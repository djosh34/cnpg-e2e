# Alertmanager of the e2e test, with the routes of alertmanager.yaml. The run
# copies the company CA in as its trusted CAs: the CA signs the receiver's
# certificate.
FROM prom/alertmanager:v0.34.1@sha256:e9733bafb1bdef9b00e25a21f8f99dc26a22224bf16641ad754d1649f4c3357a
COPY alertmanager.yaml /etc/alertmanager/alertmanager.yml
CMD ["--config.file=/etc/alertmanager/alertmanager.yml", "--storage.path=/alertmanager", "--cluster.listen-address="]
