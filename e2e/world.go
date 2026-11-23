package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// world is everything one run starts: the network, the services outside
// k3s, the three nodes, and the releases of the run's settings on them.
// newWorld installs no releases: helmfile installs them through the
// playbooks, which only the lab (lab_test.go) runs so far.
// Nothing of it outlives the e2e test's container.
type world struct {
	root    *ca
	bao     *openbao
	mon     *monitoring
	cluster chartValues // of the cluster's release

	built time.Time // when the e2e test started to build it
}

// newWorld builds the world, with the cluster on the instance image
// firstImage.
func newWorld(ctx context.Context, firstImage string) (*world, error) {
	root, err := newCA()
	if err != nil {
		return nil, err
	}
	if err := createNetwork(ctx); err != nil {
		return nil, err
	}
	w := &world{root: root, built: time.Now()}
	logf("building the client image")
	if err := buildClient(ctx); err != nil {
		return nil, err
	}
	logf("starting the DNS, the company CA, the store and the monitoring")
	if err := startDNS(ctx); err != nil {
		return nil, err
	}
	if w.bao, err = startACME(ctx, root); err != nil {
		return nil, err
	}
	if err := startStore(ctx, root); err != nil {
		return nil, err
	}
	if w.mon, err = startMonitoring(ctx, root); err != nil {
		return nil, err
	}
	logf("starting k3s")
	if err := startNodes(ctx); err != nil {
		return nil, err
	}
	// The charts and their Secrets come from the playbooks' helmfile, which
	// this runner does not run, so the scenario fails at its first look at
	// the cluster.
	logf("no charts: helmfile installs them through the playbooks, which only the lab runs")
	return w, nil
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
