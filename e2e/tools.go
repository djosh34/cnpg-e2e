package e2e

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

// installTools downloads kubectl and kubectl-cnpg of the versions file, for
// amd64, checks each download against its sha256 there, and puts both into
// dir.
func installTools(ctx context.Context, m *versions.Manifest, dir string) error {
	k, c := m.Tools.Kubectl, m.Tools.KubectlCNPG
	kubectl, err := download(ctx, "https://dl.k8s.io/release/"+k.Version+"/bin/linux/amd64/kubectl", k.SHA256["amd64"])
	if err != nil {
		return err
	}
	archive, err := download(ctx, fmt.Sprintf("https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v%s/kubectl-cnpg_%s_linux_x86_64.tar.gz",
		c.Version, c.Version), c.SHA256["amd64"])
	if err != nil {
		return err
	}
	cnpg, err := fromTarGz(archive, "kubectl-cnpg")
	if err != nil {
		return err
	}
	return errors.Join(os.WriteFile(filepath.Join(dir, "kubectl"), kubectl, 0o755),
		os.WriteFile(filepath.Join(dir, "kubectl-cnpg"), cnpg, 0o755))
}

// download fetches url and checks its sha256.
func download(ctx context.Context, url, sum string) ([]byte, error) {
	data, err := fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != sum {
		return nil, fmt.Errorf("%s has the sha256 %x, the versions file says %s", url, got, sum)
	}
	return data, nil
}

// fetch gets url, trying again for up to 3 min.
func fetch(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	var data []byte
	err := poll(ctx, time.Now().Add(3*time.Minute), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		data, err = io.ReadAll(resp.Body)
		if err := errors.Join(err, resp.Body.Close()); err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: %s", url, resp.Status)
		}
		return nil
	})
	return data, err
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
