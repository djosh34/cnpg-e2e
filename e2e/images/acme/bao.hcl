# Port 8200 serves the API to the run without TLS, on the e2e network only;
# 443 serves ACME with the certificate the run copies in.
storage "raft" {
  path    = "/openbao/file"
  node_id = "acme"
}
cluster_addr = "http://127.0.0.1:8201"
api_addr     = "https://acme.example.test"
listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = true
}
listener "tcp" {
  address       = "0.0.0.0:443"
  tls_cert_file = "/bao/tls.crt"
  tls_key_file  = "/bao/tls.key"
}
