# Grafana of the e2e test, with Prometheus as its data source and the
# dashboard of dashboard.json. The run sets the admin password.
FROM grafana/grafana:12.4.12@sha256:a3b504df900c24db125e5ec0646f3ac3428db38161302d69f05d0db70a330a90
COPY datasources.yaml /etc/grafana/provisioning/datasources/e2e.yaml
COPY dashboards.yaml /etc/grafana/provisioning/dashboards/e2e.yaml
COPY dashboard.json /etc/grafana/dashboards/dashboard.json
