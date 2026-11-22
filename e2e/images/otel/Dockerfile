# The upstream OTel service of the e2e test, the one URL the collectors in
# k3s send to: OTLP over gRPC with mTLS, handed to Prometheus by remote
# write. The run copies its TLS certificate and the company CA in.
FROM otel/opentelemetry-collector-contrib:0.163.2@sha256:7f22d2a6748110ffb6a59bc0df448bccbf501cdb667d610ede4c61d3863d5849
COPY config.yaml /etc/otel/config.yaml
CMD ["--config=/etc/otel/config.yaml"]
