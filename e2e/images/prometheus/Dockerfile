# Prometheus of the e2e test, the rule evaluator: rules.yaml on the metrics
# the OTel service writes, its alerts to Alertmanager.
FROM prom/prometheus:v3.7.3@sha256:49214755b6153f90a597adcbff0252cc61069f8ab69ce8411285cd4a560e8038
COPY prometheus.yml /etc/prometheus/prometheus.yml
COPY rules.yaml /etc/prometheus/rules.yaml
CMD ["--config.file=/etc/prometheus/prometheus.yml", "--storage.tsdb.path=/prometheus", "--web.enable-remote-write-receiver"]
