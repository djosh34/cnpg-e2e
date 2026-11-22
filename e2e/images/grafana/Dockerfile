# Grafana of the e2e test, with Prometheus as its data source and the
# dashboard of dashboard.json. The run sets the admin password.
FROM grafana/grafana:12.4.12@sha256:83be3e511ede559bee80e2215bb1987cb1246075461cb961beb515b0341e7aea
COPY datasources.yaml /etc/grafana/provisioning/datasources/e2e.yaml
COPY dashboards.yaml /etc/grafana/provisioning/dashboards/e2e.yaml
COPY dashboard.json /etc/grafana/dashboards/dashboard.json
