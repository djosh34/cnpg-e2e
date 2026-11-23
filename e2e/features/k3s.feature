Feature: k3s
  rotate-ca.yml rotates the CAs that k3s made itself, as docs.k3s.io says,
  with the script of the pinned k3s release: a new CA for the server,
  client, request-header, etcd server and etcd peer certificates, each
  cross-signed by the old one, and a new service account key that keeps the
  old one. k3s then restarts on one node after another and makes that node's
  certificates from the new CAs. The pods keep running while k3s restarts.

  Background:
    Given the cluster is healthy

  @ca-rotation
  Scenario: The k3s and etcd CAs are rotated while the client writes and reads
    When the k3s CAs are rotated with rotate-ca.yml
    Then every node is on the new CAs < 300 s
    And every node is Ready < 300 s
    And the cluster is healthy < 600 s
