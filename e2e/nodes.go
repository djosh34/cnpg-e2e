package main

import (
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
)

// stopNodes stops node containers at once, as docker stop -t 0 does, as lost
// machines.
func stopNodes(ctx context.Context, nodes ...string) error {
	if _, err := command(ctx, "docker", append([]string{"stop", "-t", "0"}, nodes...)...); err != nil {
		return err
	}
	logf("node %s stopped", strings.Join(nodes, " and "))
	return nil
}

// restartNodes starts node containers that stopNodes stopped. systemd
// starts k3s again, and k3s the pods of the node.
func restartNodes(ctx context.Context, nodes ...string) error {
	if _, err := command(ctx, "docker", append([]string{"start"}, nodes...)...); err != nil {
		return err
	}
	logf("node %s started", strings.Join(nodes, " and "))
	return nil
}

// useRunningAPI points the run's kubeconfig at the API server of the
// first node that is not down.
func useRunningAPI(down []string) error {
	for i := range nodeCount {
		if slices.Contains(down, nodeName(i)) {
			continue
		}
		data, err := os.ReadFile(kubeconfig)
		if err != nil {
			return err
		}
		server := regexp.MustCompile(`https://[0-9.]+:6443`)
		return os.WriteFile(kubeconfig, server.ReplaceAll(data, []byte("https://"+nodeIP(i)+":6443")), 0o600)
	}
	return errors.New("every node is down")
}
