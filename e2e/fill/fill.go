// Package fill gets what an install needs from outside the airgap, for
// production (cmd/fill) and the e2e alike: every file of the versions file
// in files/, the dependencies of the wrapper charts, and every image in the
// registry the nodes read. Ansible then only copies.
package fill

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
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/registry"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// Dir is where fill puts the files, from the root of the checkout.
const Dir = "files"

// Run fills Dir and the registry at host from the versions file data.
func Run(ctx context.Context, data []byte, host string, plainHTTP bool) error {
	m, err := versions.Parse(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir, 0o755); err != nil {
		return err
	}
	for _, f := range []struct {
		name, key string
		d         versions.Download
	}{
		{"k3s", "k3s.binary", m.K3s.Binary},
		{"k3s-install.sh", "k3s.install", m.K3s.Install},
		{"k3s-airgap-images-amd64.tar.zst", "k3s.airgap", m.K3s.Airgap},
		{"k3s-rotate-ca.sh", "k3s.rotate-ca", m.K3s.RotateCA},
		{"container-selinux.rpm", "selinux.container-selinux", m.SELinux["container-selinux"]},
		{"k3s-selinux.rpm", "selinux.k3s-selinux", m.SELinux["k3s-selinux"]},
		{"helm.tar.gz", "nodeTools.helm", m.NodeTools["helm"]},
		{"helm-diff.tgz", "nodeTools.helm-diff", m.NodeTools["helm-diff"]},
		{"helmfile.tar.gz", "nodeTools.helmfile", m.NodeTools["helmfile"]},
	} {
		if f.d.URL == "" || f.d.SHA256 == "" {
			return fmt.Errorf("no %s with url and sha256 in %s", f.key, versions.File)
		}
		if err := Download(ctx, f.d, filepath.Join(Dir, f.name)); err != nil {
			return err
		}
	}
	for _, name := range []string{"helm", "helm-diff", "helmfile"} {
		archive := filepath.Join(Dir, name+".tar.gz")
		if name == "helm-diff" {
			archive = filepath.Join(Dir, name+".tgz")
		}
		if err := Untar(archive, filepath.Join(Dir, name)); err != nil {
			return err
		}
	}
	for key, c := range m.Charts {
		if err := wrapper(ctx, filepath.Join("helm", "charts", "cnpg-"+key), c.Version); err != nil {
			return err
		}
	}
	if err := pack(data, filepath.Join(Dir, "helm.tar")); err != nil {
		return err
	}
	var plain []string
	if plainHTTP {
		plain = append(plain, host)
	}
	rc := registry.Client(plain...)
	var archive bytes.Buffer
	if err := registry.Export(ctx, rc, m.Registry.Image, &archive); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(Dir, "registry.tar"), archive.Bytes(), 0o644); err != nil {
		return err
	}
	return registry.Copy(ctx, rc, m.Pinned, host)
}

// Download fetches d into file and checks its sha256, unless file has it
// already. A GET that gets no answer or a server error is tried up to five
// times in ten minutes: hosts such as raw.githubusercontent.com answer 503
// now and then.
func Download(ctx context.Context, d versions.Download, file string) error {
	if data, err := os.ReadFile(file); err == nil && sum(data) == d.SHA256 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	data, status, err := get(ctx, d.URL)
	for try := 1; err != nil && status/100 != 4 && try < 5; try++ {
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(time.Duration(try) * 5 * time.Second):
		}
		data, status, err = get(ctx, d.URL)
	}
	if err != nil {
		return err
	}
	if got := sum(data); got != d.SHA256 {
		return fmt.Errorf("%s has the sha256 %s, not %s", d.URL, got, d.SHA256)
	}
	return os.WriteFile(file, data, 0o644)
}

// get returns the body of url, or the status of an answer other than 200.
func get(ctx context.Context, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, errors.Join(fmt.Errorf("GET %s: %s", url, resp.Status), resp.Body.Close())
	}
	data, err := io.ReadAll(resp.Body)
	return data, 0, errors.Join(err, resp.Body.Close())
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// Untar unpacks a tar.gz archive into dir, anew.
func Untar(archive, dir string) error {
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%s: %w", archive, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	r := tar.NewReader(z)
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", archive, err)
		}
		path := filepath.Join(dir, h.Name)
		if !strings.HasPrefix(path, filepath.Clean(dir)+string(os.PathSeparator)) {
			return fmt.Errorf("%s: %s is outside the archive", archive, h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(path, 0o755)
		case tar.TypeReg:
			var data []byte
			if data, err = io.ReadAll(r); err == nil {
				err = errors.Join(os.MkdirAll(filepath.Dir(path), 0o755), os.WriteFile(path, data, fs.FileMode(h.Mode)&0o755|0o644))
			}
		}
		if err != nil {
			return err
		}
	}
}

// The version of the one dependency, the only indented version.
var dependencyVersion = regexp.MustCompile(`(?m)^([ \t]+version:).*$`)

// wrapper sets the version of the upstream chart in the wrapper chart's
// Chart.yaml and builds its charts/ with helm dependency build.
func wrapper(ctx context.Context, dir, version string) error {
	file := filepath.Join(dir, "Chart.yaml")
	chart, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	chart = dependencyVersion.ReplaceAll(chart, []byte(`$1 "`+version+`"`))
	err = errors.Join(os.WriteFile(file, chart, 0o644), os.RemoveAll(filepath.Join(dir, "charts")), os.RemoveAll(filepath.Join(dir, "Chart.lock")))
	if err != nil {
		return err
	}
	helm := filepath.Join(Dir, "helm", "linux-amd64", "helm")
	out, err := exec.CommandContext(ctx, helm, "dependency", "build", dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm dependency build %s: %w: %s", dir, err, out)
	}
	return nil
}

// pack writes helm/ and the versions file data into one tar archive, laid
// out as in the checkout, the same bytes for the same content. Chart.lock
// holds the time of the build, which the install needs not.
func pack(data []byte, file string) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, mode int64, content []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), ModTime: time.Unix(0, 0)}); err != nil {
			return err
		}
		_, err := tw.Write(content)
		return err
	}
	err := filepath.WalkDir("helm", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "Chart.lock" {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return add(filepath.ToSlash(path), 0o644, content)
	})
	if err := errors.Join(err, add(versions.File, 0o644, data), tw.Close()); err != nil {
		return err
	}
	return os.WriteFile(file, buf.Bytes(), 0o644)
}
