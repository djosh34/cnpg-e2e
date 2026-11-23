package main

import (
	"context"
	"time"
)

// chartValues are the directory of a release's chart and its --values and
// --set arguments.
type chartValues struct {
	chart  string
	values []string
}

func helm(ctx context.Context, limit time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return command(ctx, "helm", args...)
}
