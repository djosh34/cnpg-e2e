# Prometheus of the e2e test, the rule evaluator: rules.yaml on the metrics
# the OTel service writes, its alerts to Alertmanager.
FROM prom/prometheus:v3.7.3@sha256:6573196e5dbe834779ac4d34cb053113cc502a69e9e20ba3be96d37f33e81545
COPY prometheus.yml /etc/prometheus/prometheus.yml
COPY rules.yaml /etc/prometheus/rules.yaml
CMD ["--config.file=/etc/prometheus/prometheus.yml", "--storage.tsdb.path=/prometheus", "--web.enable-remote-write-receiver"]
