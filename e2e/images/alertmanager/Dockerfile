# Alertmanager of the e2e test, with the routes of alertmanager.yaml. The run
# copies the company CA in as its trusted CAs: the CA signs the receiver's
# certificate.
FROM prom/alertmanager:v0.34.1@sha256:84967b9b7ba45e38a9278d3e594305f43d4993c310df3905b51138b816c365f3
COPY alertmanager.yaml /etc/alertmanager/alertmanager.yml
CMD ["--config.file=/etc/alertmanager/alertmanager.yml", "--storage.path=/alertmanager", "--cluster.listen-address="]
