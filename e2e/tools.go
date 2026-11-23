package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/djosh34/cnpg-e2e/e2e/fill"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// installTools puts kubectl and kubectl-cnpg of the versions file into this
// user's cache, first on PATH.
func installTools(ctx context.Context, lab versions.Lab) error {
	dir, err := cacheFile("tools")
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return err
	}
	cnpg := lab.KubectlCNPG.Version
	kubectl := filepath.Join(dir, "kubectl")
	err = fill.Download(ctx, versions.Download{
		URL:    "https://dl.k8s.io/release/" + lab.Kubectl.Version + "/bin/linux/amd64/kubectl",
		SHA256: lab.Kubectl.SHA256,
	}, kubectl)
	if err == nil {
		err = os.Chmod(kubectl, 0o755)
	}
	if err == nil {
		err = fill.Download(ctx, versions.Download{
			URL:    fmt.Sprintf("https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v%s/kubectl-cnpg_%s_linux_x86_64.tar.gz", cnpg, cnpg),
			SHA256: lab.KubectlCNPG.SHA256,
		}, filepath.Join(dir, "kubectl-cnpg.tar.gz"))
	}
	if err == nil {
		err = fill.Untar(filepath.Join(dir, "kubectl-cnpg.tar.gz"), filepath.Join(dir, "cnpg"))
	}
	if err != nil {
		return err
	}
	return os.Setenv("PATH", dir+string(os.PathListSeparator)+filepath.Join(dir, "cnpg")+string(os.PathListSeparator)+os.Getenv("PATH"))
}
