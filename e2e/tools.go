package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// installTools puts kubectl and kubectl-cnpg of the versions file into a
// directory of this user's cache, first on PATH.
func installTools(ctx context.Context, lab versions.Lab) error {
	dir, err := cacheFile(lab.Kubectl.Version + "-" + lab.KubectlCNPG.Version)
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return err
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return err
	}
	kubectl := filepath.Join(dir, "kubectl")
	if _, err := os.Stat(kubectl); err != nil {
		data, err := download(ctx, "https://dl.k8s.io/release/"+lab.Kubectl.Version+"/bin/linux/amd64/kubectl", lab.Kubectl.SHA256)
		if err != nil {
			return err
		}
		if err := os.WriteFile(kubectl, data, 0o755); err != nil {
			return err
		}
	}
	cnpg := filepath.Join(dir, "kubectl-cnpg")
	if _, err := os.Stat(cnpg); err == nil {
		return nil
	}
	v := lab.KubectlCNPG.Version
	archive, err := download(ctx, fmt.Sprintf("https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v%s/kubectl-cnpg_%s_linux_x86_64.tar.gz", v, v), lab.KubectlCNPG.SHA256)
	if err != nil {
		return err
	}
	data, err := fromTarGz(archive, "kubectl-cnpg")
	if err != nil {
		return err
	}
	return os.WriteFile(cnpg, data, 0o755)
}

// download fetches url and checks its sha256.
func download(ctx context.Context, url, sum string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	if err := errors.Join(err, resp.Body.Close()); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != sum {
		return nil, fmt.Errorf("%s has the sha256 %x, the versions file says %s", url, got, sum)
	}
	return data, nil
}

// fromTarGz is the file name of a tar.gz archive.
func fromTarGz(archive []byte, name string) ([]byte, error) {
	z, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	r := tar.NewReader(z)
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the archive has no %s", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Name == name {
			return io.ReadAll(r)
		}
	}
}
