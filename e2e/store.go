package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/testcontainers/testcontainers-go"
)

// The store: versitygw with TLS, a certificate of the company CA. Its
// bucket is versioned, with a policy that lets CNPG's keys list, read and
// write, and refuses everyone deletes and changes of the bucket. Its root
// keys pass every policy, so only the e2e has them. The admin API, which
// makes users, listens on the container's loopback.
const (
	s3Bucket     = "example-bucket"
	s3RootAccess = "example-root-access-key"
	s3RootSecret = "example-root-secret-key"
	// CNPG's keys.
	s3Access = "example-access-key"
	s3Secret = "example-secret-key"

	s3Region = "us-east-1"
	s3Admin  = "http://127.0.0.1:7070"
)

var s3Policy = `{"Version": "2012-10-17", "Statement": [
  {"Effect": "Allow", "Principal": {"AWS": ["` + s3Access + `"]}, "Action": "s3:ListBucket",
   "Resource": "arn:aws:s3:::` + s3Bucket + `"},
  {"Effect": "Allow", "Principal": {"AWS": ["` + s3Access + `"]},
   "Action": ["s3:GetObject", "s3:PutObject", "s3:AbortMultipartUpload"],
   "Resource": "arn:aws:s3:::` + s3Bucket + `/*"},
  {"Effect": "Deny", "Principal": "*",
   "Action": ["s3:DeleteObject", "s3:DeleteObjectVersion", "s3:BypassGovernanceRetention",
              "s3:PutObjectRetention", "s3:PutObjectLegalHold"],
   "Resource": "arn:aws:s3:::` + s3Bucket + `/*"},
  {"Effect": "Deny", "Principal": "*",
   "Action": ["s3:DeleteBucket", "s3:PutLifecycleConfiguration", "s3:PutBucketVersioning",
              "s3:PutBucketObjectLockConfiguration", "s3:PutBucketPolicy", "s3:DeleteBucketPolicy"],
   "Resource": "arn:aws:s3:::` + s3Bucket + `"}
]}`

const versioningOn = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`

// The arguments of versitygw. Its metadata goes to sidecar files, not
// extended attributes, which docker commit drops.
var s3Args = []string{"--access", s3RootAccess, "--secret", s3RootSecret, "--port", ":443",
	"--cert", "/tls/tls.crt", "--key", "/tls/tls.key", "--admin-port", strings.TrimPrefix(s3Admin, "http://"),
	"--iam-dir", "/s3/iam", "posix", "--versioning-dir", "/s3/versions", "--sidecar", "/s3/meta", "/s3/data"}

// s3User makes a user of the store with the keys.
func s3User(ctx context.Context, access, secret string) error {
	_, err := command(ctx, "docker", "exec", "s3", "/usr/local/bin/versitygw", "admin", "--access", s3RootAccess,
		"--secret", s3RootSecret, "--endpoint-url", s3Admin, "create-user", "--access", access, "--secret", secret, "--role", "user")
	return err
}

// setUpStore makes CNPG's user and the bucket, once the store answers, and
// checks that CNPG's keys can write but not delete or change the bucket.
func setUpStore(ctx context.Context, rootKeys, cnpg s3Client) error {
	err := poll(ctx, time.Now().Add(time.Minute), func(ctx context.Context) error {
		return rootKeys.want(ctx, http.MethodGet, "/", "", 200)
	})
	if err != nil {
		return fmt.Errorf("the store answering: %w", err)
	}
	if err := s3User(ctx, s3Access, s3Secret); err != nil {
		return err
	}
	bucket, check := "/"+s3Bucket, "/"+s3Bucket+"/e2e/limits-check"
	steps := []struct {
		keys         s3Client
		method, path string
		body         string
		status       int
	}{
		{rootKeys, http.MethodPut, bucket, "", 200},
		{rootKeys, http.MethodPut, bucket + "?versioning", versioningOn, 200},
		{rootKeys, http.MethodPut, bucket + "?policy", s3Policy, 204},
		{cnpg, http.MethodPut, check, "check", 200},
		{cnpg, http.MethodGet, check, "", 200},
		{cnpg, http.MethodDelete, check, "", 403},
		{cnpg, http.MethodPut, bucket + "?versioning", strings.ReplaceAll(versioningOn, "Enabled", "Suspended"), 403},
		{cnpg, http.MethodPut, bucket + "?policy", `{"Version": "2012-10-17", "Statement": []}`, 403},
		{cnpg, http.MethodDelete, bucket + "?policy", "", 403},
		{cnpg, http.MethodDelete, bucket, "", 403},
	}
	for _, st := range steps {
		if err := st.keys.want(ctx, st.method, st.path, st.body, st.status); err != nil {
			return fmt.Errorf("the store's bucket: %w", err)
		}
	}
	return nil
}

// s3Client signs its requests to the store with one key.
type s3Client struct {
	http *http.Client
	keys aws.Credentials
}

// newS3 is a client of the store that trusts the company CA. It reaches the
// store by its address, which this machine's resolver does not know by
// name.
func newS3(root *ca, access, secret string) s3Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(root.certPEM)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: s3Name, MinVersion: tls.VersionTLS12}}
	return s3Client{
		http: &http.Client{Timeout: 30 * time.Second, Transport: transport},
		keys: aws.Credentials{AccessKeyID: access, SecretAccessKey: secret},
	}
}

// want sends the request and checks the answer's status.
func (c s3Client) want(ctx context.Context, method, path, body string, status int) error {
	req, err := http.NewRequestWithContext(ctx, method, "https://"+s3IP+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", hash)
	if json.Valid([]byte(body)) {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := v4.NewSigner().SignHTTP(ctx, c.keys, req, hash, "s3", s3Region, time.Now()); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	answer, err := io.ReadAll(resp.Body)
	if err := errors.Join(err, resp.Body.Close()); err != nil {
		return err
	}
	if resp.StatusCode != status {
		return fmt.Errorf("%s %s with the key %s: %s, not %d: %s", method, path, c.keys.AccessKeyID, resp.Status, status,
			bytes.TrimSpace(answer))
	}
	return nil
}

// The store breaks in one of two ways. Stopped, it refuses connections: its
// address belongs to a container that listens on no port. Paused, it takes
// connections and never answers. Either way it keeps its files, users and
// settings. The stand-in runs sleep of image, an image of the lab that has
// it, on the lab's network. restoreStore removes it, and Ryuk does when the
// test is killed, as it does the lab's containers: the stand-in has their
// labels.
const storeGone = "s3-gone"

func stopStore(ctx context.Context, network, image string) error {
	if _, err := command(ctx, "docker", "stop", "-t", "0", "s3"); err != nil {
		return err
	}
	args := []string{"run", "--detach", "--name", storeGone, "--network", network, "--ip", s3IP}
	labels := testcontainers.GenericLabels()
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		args = append(args, "--label", k+"="+labels[k])
	}
	_, err := command(ctx, "docker", append(args, "--entrypoint", "sleep", image, "infinity")...)
	return err
}

func pauseStore(ctx context.Context) error {
	_, err := command(ctx, "docker", "pause", "s3")
	return err
}

// restoreStore brings back a store that stopStore stopped or pauseStore
// paused, also when the break failed halfway: a store that was never
// paused, a stand-in that was never made, or a store that still runs.
func restoreStore(ctx context.Context, stopped bool) error {
	if !stopped {
		state, err := command(ctx, "docker", "inspect", "-f", "{{.State.Paused}}", "s3")
		if err != nil || state != "true" {
			return err
		}
		_, err = command(ctx, "docker", "unpause", "s3")
		return err
	}
	if _, err := command(ctx, "docker", "rm", "-f", storeGone); err != nil && !strings.Contains(err.Error(), "No such container") {
		return err
	}
	_, err := command(ctx, "docker", "start", "s3")
	return err
}
