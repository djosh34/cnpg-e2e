package e2e

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// world is what a feature scenario runs on: the lab, started from the
// cache, with every chart deployed by upgrade.yml, the versions of the run,
// the charts' secrets that upgrade.yml got, and the monitoring.
type world struct {
	lab *lab
	m   *versions.Manifest
	env map[string]string
	mon *monitoring

	built time.Time // when the e2e test started to build it
}

// takeBackup starts a backup of the cluster to the store, through the
// barman-cloud plugin.
func takeBackup(ctx context.Context, name string) error {
	_, err := kubectl(ctx, "cnpg", "backup", clusterName, "-n", namespace, "--backup-name", name,
		"--method", "plugin", "--plugin-name", "barman-cloud.cloudnative-pg.io")
	return err
}

func backupCompleted(ctx context.Context, name string) error {
	out, err := kubectl(ctx, "get", "backup", name, "-n", namespace, "-o", `jsonpath={.status.phase}{"\t"}{.status.error}`)
	if err != nil {
		return err
	}
	if phase, why, _ := strings.Cut(out, "\t"); phase != "completed" {
		return fmt.Errorf("the backup %s is %q %s", name, phase, why)
	}
	return nil
}

// roles names the instances: the primary, and the replicas by pod name.
// Until a change of primary, a replica is replica 1.
func roles(ctx context.Context) (map[string]string, error) {
	primary, err := clusterStatus(ctx, clusterName, "currentPrimary")
	if err != nil {
		return nil, err
	}
	pods, err := instances(ctx, clusterName)
	if err != nil {
		return nil, err
	}
	replicas := slices.DeleteFunc(pods, func(p string) bool { return p == primary })
	if len(replicas) != nodeCount-1 {
		return nil, fmt.Errorf("instances besides the primary %s: %v", primary, replicas)
	}
	return map[string]string{
		"the primary": primary, "replica 1": replicas[0], "replica 2": replicas[1], "a replica": replicas[0],
	}, nil
}
