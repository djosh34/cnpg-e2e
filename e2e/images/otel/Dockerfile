# The upstream OTel service of the e2e test, the one URL the collectors in
# k3s send to: OTLP over gRPC with mTLS, handed to Prometheus by remote
# write. The run copies its TLS certificate and the company CA in.
FROM otel/opentelemetry-collector-contrib:0.163.2@sha256:989669581ee1f89184f3fd9dd5adfc6615ed6c9692c026aaea89e07a20dfb177
COPY config.yaml /etc/otel/config.yaml
CMD ["--config=/etc/otel/config.yaml"]
