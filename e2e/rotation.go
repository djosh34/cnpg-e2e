package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The CA rotation is the scenario @ca-rotation of the feature files:
// ansible/rotate-ca.yml rotates the CAs that k3s made itself while the
// client writes and reads, and every node then serves and uses certificates
// of the new CAs.

// Where k3s keeps its server files on a node.
const k3sServerDir = "/var/lib/rancher/k3s/server"

// rotateCAs: rotate-ca.yml runs on the nodes.
func rotateCAs(ctx context.Context, s *scenario, _ args) error {
	out, err := s.lab.playbook(ctx, nil, "rotate-ca.yml")
	if err != nil {
		return err
	}
	s.detail = oneLine(recap(out))
	return nil
}

// newCAs are the new CAs of the first node, by the file names of the CAs
// under server/tls: the Kubernetes server and client CAs, the API
// aggregation CA, and etcd's server and peer CAs. After the rotation each
// file holds the new CA, then the new root that signed it, then that root
// cross-signed by the old one, then the old root. Each CA's options take
// the new root as the only root, so only a certificate of the new CA
// verifies.
func newCAs(ctx context.Context) (map[string]x509.VerifyOptions, error) {
	pools := map[string]x509.VerifyOptions{}
	for _, name := range []string{"server", "client", "request-header", "etcd/server", "etcd/peer"} {
		data, err := command(ctx, "docker", "exec", nodeName(0), "cat", k3sServerDir+"/tls/"+name+"-ca.crt")
		if err != nil {
			return nil, err
		}
		var certs []*x509.Certificate
		for rest := []byte(data); ; {
			var block *pem.Block
			if block, rest = pem.Decode(rest); block == nil {
				break
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, err
			}
			certs = append(certs, cert)
		}
		if len(certs) < 2 {
			return nil, fmt.Errorf("the %s CA of %s has %d certificates: no new CA", name, nodeName(0), len(certs))
		}
		opts := x509.VerifyOptions{
			Roots: x509.NewCertPool(), Intermediates: x509.NewCertPool(),
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		}
		opts.Intermediates.AddCert(certs[0])
		opts.Roots.AddCert(certs[1])
		pools[name] = opts
	}
	return pools, nil
}

// everyNodeOnNewCAs: what each node serves and uses comes from the new CAs
// (newCAs). Its API server and kubelet serve a certificate of the new
// server CA, etcd's client and peer ports one of the new etcd server and
// peer CAs. The node's own admin kubeconfig has a client certificate of the
// new client CA, and the node's API server takes it. The API server's proxy
// certificate is of the new request-header CA, and a request through it to
// metrics-server works. A request through the API server to the node's
// kubelet works, which needs each to take the other's new certificate.
func everyNodeOnNewCAs(ctx context.Context, _ *scenario, _ args) error {
	cas, err := newCAs(ctx)
	if err != nil {
		return err
	}
	for i := range nodeCount {
		if err := onNewCAs(ctx, cas, i); err != nil {
			return err
		}
	}
	return nil
}

func onNewCAs(ctx context.Context, cas map[string]x509.VerifyOptions, i int) error {
	node := nodeName(i)
	for _, l := range []struct{ what, port, ca string }{
		{"the API server", "6443", "server"},
		{"the kubelet", "10250", "server"},
		{"etcd", "2379", "etcd/server"},
		{"etcd's peer port", "2380", "etcd/peer"},
	} {
		cert, err := servedCert(ctx, net.JoinHostPort(nodeIP(i), l.port))
		if err == nil {
			err = verifyNew(cas, l.ca, cert)
		}
		if err != nil {
			return fmt.Errorf("%s of %s: %w", l.what, node, err)
		}
	}
	config, admin, err := nodeKubeconfig(ctx, i)
	if err == nil {
		err = verifyNew(cas, "client", admin)
	}
	if err != nil {
		return fmt.Errorf("the admin kubeconfig of %s: %w", node, err)
	}
	proxy, err := nodeCert(ctx, node, k3sServerDir+"/tls/client-auth-proxy.crt")
	if err == nil {
		err = verifyNew(cas, "request-header", proxy)
	}
	if err != nil {
		return fmt.Errorf("the API server's proxy certificate of %s: %w", node, err)
	}
	for _, path := range []string{"/apis/metrics.k8s.io/v1beta1/nodes", "/api/v1/nodes/" + node + "/proxy/healthz"} {
		if _, err := kubectl(ctx, "--kubeconfig", config, "get", "--raw", path); err != nil {
			return err
		}
	}
	return nil
}

// servedCert is the certificate a TLS port serves. The check of it is
// verifyNew's, against the new CAs alone.
func servedCert(ctx context.Context, addr string) (*x509.Certificate, error) {
	var cert *x509.Certificate
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config: &tls.Config{
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("no certificate")
				}
				var err error
				cert, err = x509.ParseCertificate(raw[0])
				return err
			},
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		// etcd ends the handshake when no client certificate comes, after
		// it sent its own.
		if cert != nil {
			return cert, nil
		}
		return nil, err
	}
	return cert, conn.Close()
}

// verifyNew checks that the new CA of that name issued the certificate.
func verifyNew(cas map[string]x509.VerifyOptions, ca string, cert *x509.Certificate) error {
	if _, err := cert.Verify(cas[ca]); err != nil {
		return fmt.Errorf("%q, issued by %q, is not of the new %s CA: %w", cert.Subject.CommonName, cert.Issuer.CommonName, ca, err)
	}
	return nil
}

// firstCert is the first certificate of PEM data.
func firstCert(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no certificate first in the PEM data")
	}
	return x509.ParseCertificate(block.Bytes)
}

// nodeCert is the certificate in a file of the node.
func nodeCert(ctx context.Context, node, path string) (*x509.Certificate, error) {
	data, err := command(ctx, "docker", "exec", node, "cat", path)
	if err != nil {
		return nil, err
	}
	return firstCert([]byte(data))
}

var clientCertData = regexp.MustCompile(`client-certificate-data: (\S+)`)

// nodeKubeconfig is a copy of the admin kubeconfig k3s wrote on node i, at
// that node's API server, next to the run's kubeconfig, and its client
// certificate.
func nodeKubeconfig(ctx context.Context, i int) (string, *x509.Certificate, error) {
	data, err := command(ctx, "docker", "exec", nodeName(i), "cat", "/etc/rancher/k3s/k3s.yaml")
	if err != nil {
		return "", nil, err
	}
	m := clientCertData.FindStringSubmatch(data)
	if m == nil {
		return "", nil, errors.New("no client-certificate-data")
	}
	certPEM, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return "", nil, err
	}
	cert, err := firstCert(certPEM)
	if err != nil {
		return "", nil, err
	}
	config := filepath.Join(filepath.Dir(kubeconfig), "kubeconfig-"+nodeName(i))
	data = strings.ReplaceAll(data, "https://127.0.0.1:6443", "https://"+nodeIP(i)+":6443")
	return config, cert, os.WriteFile(config, []byte(data), 0o600)
}
