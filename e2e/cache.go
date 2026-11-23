package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// The cache is a running cluster that install saved on a commit of main:
// an image of each node, the store and the CA, committed once k3s-killall.sh
// stopped every container, a crash-consistent copy. A run starts from the
// cache of its merge base with main and runs install.yml on it, which
// catches it up.

// cachedNames are the containers the cache holds.
var cachedNames = []string{"s3", "acme", "k3s-1", "k3s-2", "k3s-3"}

func cacheRef(name, commit string) string {
	return versions.Packages + "/cache-" + name + ":" + commit
}

// The file in the CA container with what a restored lab needs besides the
// containers: the CA, OpenBao's keys and the install's secrets, whose EAB
// keys its ACME accounts registered with, so a restored cluster gets the
// same values.
const savedFile = "/bao/saved.json"

type saved struct {
	CACert string            `json:"caCert"`
	CAKey  string            `json:"caKey"`
	Unseal string            `json:"unseal"`
	Token  string            `json:"token"`
	Env    map[string]string `json:"env"`
}

// cached is a pulled cache: its images by container, and what it saved.
type cached struct {
	saved
	images map[string]string
}

// mergeBase is the commit of main whose cache a run starts from.
func mergeBase(ctx context.Context) (string, error) {
	return command(ctx, "git", "merge-base", "HEAD", "origin/main")
}

// pull pulls the cache of commit and reads what it saved.
func pull(ctx context.Context, commit string) (*cached, error) {
	c := &cached{images: map[string]string{}}
	for _, name := range cachedNames {
		c.images[name] = cacheRef(name, commit)
		if _, err := command(ctx, "docker", "pull", "--quiet", c.images[name]); err != nil {
			return nil, err
		}
	}
	data, err := command(ctx, "docker", "run", "--rm", "--entrypoint", "cat", c.images["acme"], savedFile)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(data), &c.saved); err != nil {
		return nil, fmt.Errorf("%s of %s: %w", savedFile, c.images["acme"], err)
	}
	return c, nil
}

// save writes a row into the database, stops the cluster and
// pushes it as the cache of the merge base, which on main is the commit
// itself. A restored cluster reads the row back.
func (s *scenario) save(ctx context.Context) error {
	commit, err := mergeBase(ctx)
	if err != nil {
		return err
	}
	logf("saving the cluster as the cache of %s", commit)
	primary, err := clusterStatus(ctx, clusterName, "currentPrimary")
	if err != nil {
		return err
	}
	_, err = psql(ctx, primary, "postgres", "create table e2e_cache (saved timestamptz default now(), commit text); "+
		"insert into e2e_cache (commit) values ('"+commit+"')")
	if err != nil {
		return err
	}
	l := s.lab
	for i := range nodeCount {
		if code, out, err := l.onNode(ctx, i, "k3s-killall.sh"); err != nil || code != 0 {
			return errors.Join(err, fmt.Errorf("k3s-killall.sh on %s: exit code %d\n%s", nodeName(i), code, out))
		}
	}
	data, err := json.Marshal(saved{
		CACert: string(l.root.certPEM), CAKey: string(l.root.keyPEM), Unseal: l.bao.unseal, Token: l.bao.token, Env: l.env,
	})
	if err != nil {
		return err
	}
	if err := l.containers["acme"].CopyToContainer(ctx, data, savedFile, 0o644); err != nil {
		return err
	}
	timeout := time.Minute
	for _, name := range cachedNames {
		if err := l.containers[name].Stop(ctx, &timeout); err != nil {
			return fmt.Errorf("stopping %s: %w", name, err)
		}
		// docker commit copies the container's labels, and Ryuk removes
		// what has the run's session.
		image := cacheRef(name, commit)
		if _, err := command(ctx, "docker", "commit", "--change", "LABEL org.testcontainers.sessionId=", name, image); err != nil {
			return err
		}
		if name != "acme" && name != "s3" {
			if err := withStorage(ctx, name, image); err != nil {
				return err
			}
		}
		if _, err := command(ctx, "docker", "push", "--quiet", image); err != nil {
			return err
		}
		logf("pushed %s", image)
	}
	return nil
}

// withStorage copies the node's storage volume, which docker commit leaves
// out, into its image at the same path: a new volume there starts with it.
func withStorage(ctx context.Context, node, image string) (err error) {
	layer := node + "-save"
	if _, err := command(ctx, "docker", "create", "--name", layer, image); err != nil {
		return err
	}
	defer func() {
		_, rmErr := command(context.WithoutCancel(ctx), "docker", "rm", layer)
		err = errors.Join(err, rmErr)
	}()
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	var fromOut, toOut bytes.Buffer
	from := exec.CommandContext(ctx, "docker", "cp", node+":"+storageDir, "-")
	to := exec.CommandContext(ctx, "docker", "cp", "--archive", "-", layer+":/var/lib/rancher/k3s/")
	from.Stdout, from.Stderr, to.Stdin, to.Stdout, to.Stderr = w, &fromOut, r, &toOut, &toOut
	err = errors.Join(from.Start(), to.Start(), w.Close(), r.Close())
	if err = errors.Join(err, from.Wait(), to.Wait()); err != nil {
		return fmt.Errorf("copying the storage of %s: %w: %s %s", node, err, fromOut.String(), toOut.String())
	}
	_, err = command(ctx, "docker", "commit", layer, image)
	return err
}

// ready waits until every node is Ready with a heartbeat from after since:
// a restored etcd still holds the Ready of before the save.
func ready(ctx context.Context, since time.Time) error {
	query := `jsonpath={range .items[*]}{.metadata.name} {.status.conditions[?(@.type=="Ready")].status} ` +
		`{.status.conditions[?(@.type=="Ready")].lastHeartbeatTime}{"\n"}{end}`
	return poll(ctx, time.Now().Add(5*time.Minute), func(ctx context.Context) error {
		out, err := kubectl(ctx, "get", "nodes", "-o", query)
		if err != nil {
			return err
		}
		var n int
		for line := range strings.Lines(out) {
			var name, status, beat string
			if _, err := fmt.Sscan(line, &name, &status, &beat); err != nil || status != "True" {
				return fmt.Errorf("a node is not Ready: %q", line)
			}
			at, err := time.Parse(time.RFC3339, beat)
			if err != nil {
				return err
			}
			if at.Before(since.Truncate(time.Second)) {
				return fmt.Errorf("%s has no heartbeat since the start", name)
			}
			n++
		}
		if n != nodeCount {
			return fmt.Errorf("%d nodes, not %d", n, nodeCount)
		}
		return nil
	})
}

// parseCA reads a CA from its PEM certificate and key, as newCA writes them.
func parseCA(certPEM, keyPEM []byte) (*ca, error) {
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, errors.New("the CA's certificate or key is not PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the CA's key is a %T, not ECDSA", parsed)
	}
	return &ca{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}, nil
}
