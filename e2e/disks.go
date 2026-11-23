package main

import (
	"context"
	"fmt"
	"strconv"
)

// In data-disk-full each node keeps the instances' data on a tmpfs of
// diskSize, as a data disk of production, which the ballast fills.
const (
	diskSize = 1 << 30
	// The ballast file, next to the local-path volumes.
	ballastName = "e2e-ballast"
)

// dataDisks: "the data disks are mounted", before the charts.
func dataDisks(ctx context.Context, _ *scenario, _ args) error {
	for i := range nodeCount {
		_, err := command(ctx, "docker", "exec", nodeName(i), "mount", "-t", "tmpfs",
			"-o", fmt.Sprintf("size=%d,mode=0700", diskSize), "e2e-data", storageDir)
		if err != nil {
			return err
		}
	}
	return nil
}

// diskUse is the bytes used on a node's data filesystem and its size, as
// statfs and so the kubelet count them. It fails on a node whose storage
// directory is not on a data filesystem of dataDisks, so a ballast never
// fills the machine's disk.
func diskUse(ctx context.Context, node string) (used, size uint64, _ error) {
	out, err := command(ctx, "docker", "exec", node, "stat", "--file-system", "--format", "%T %S %b %f", storageDir)
	if err != nil {
		return 0, 0, err
	}
	var kind string
	var block, blocks, free uint64
	if _, err := fmt.Sscan(out, &kind, &block, &blocks, &free); err != nil {
		return 0, 0, fmt.Errorf("the data filesystem of %s: %q: %w", node, out, err)
	}
	if kind != "tmpfs" {
		return 0, 0, fmt.Errorf("the storage directory of %s is on %s, not on a data filesystem of its own", node, kind)
	}
	return (blocks - free) * block, blocks * block, nil
}

// addBallast appends n bytes to the ballast file on a node's data
// filesystem. A tmpfs counts written bytes as used at once.
func addBallast(ctx context.Context, node string, n uint64) error {
	_, err := command(ctx, "docker", "exec", node, "sh", "-c", `head -c "$1" /dev/zero >>"$2"`, "sh",
		strconv.FormatUint(n, 10), storageDir+"/"+ballastName)
	return err
}

func gib(bytes uint64) string { return fmt.Sprintf("%.1f GiB", float64(bytes)/(1<<30)) }

func share(used, size uint64) string {
	if size == 0 {
		return "-"
	}
	return fmt.Sprintf("%s (%.0f %%)", gib(used), 100*float64(used)/float64(size))
}
