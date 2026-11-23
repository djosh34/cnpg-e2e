Feature: k3s CA rotation
  rotate-ca.yml rotates the CAs that k3s made itself with k3s's script: a
  new CA for the server, client, request-header, etcd server and etcd peer
  certificates, each cross-signed by the old one. k3s then restarts on one
  node after another and makes the node's certificates from the new CAs.
  The pods keep running.

  @ca-rotation
  Scenario: The k3s and etcd CAs are rotated while the client writes and reads
    Given a running cluster
    When the k3s CAs are rotated with rotate-ca.yml
    Then every node is on the new CAs
