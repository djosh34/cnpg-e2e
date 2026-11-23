// Command e2e runs one scenario of e2e/features on this machine's Docker,
// from the root of the checkout, with only Go and Docker:
//
//	go run ./e2e <scenario>
//	go run ./e2e list [<scenario>...]
//	go run ./e2e -push install
//	go run ./e2e report
//
// list prints the scenarios as JSON, all or those named. -push saves the
// cluster after install passed as the cache of this commit and pushes it,
// which needs docker login to ghcr.io. report prints the report.yaml of
// every scenario in results/. A run writes its files to
// results/<scenario>/.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"
)

const featuresDir = "e2e/features"

func main() {
	push := flag.Bool("push", false, "save the cluster after install passed as the cache of this commit, and push it")
	flag.Parse()
	err := run(*push, flag.Args())
	if err != nil {
		say(os.Stderr, "e2e: %v\n", err)
		os.Exit(1)
	}
}

func run(push bool, args []string) error {
	all, err := scenarios()
	if err != nil {
		return err
	}
	switch {
	case len(args) > 0 && args[0] == "list":
		names := args[1:]
		if len(names) == 0 {
			names = all
		}
		for _, name := range names {
			if !slices.Contains(all, name) {
				return fmt.Errorf("no scenario %s", name)
			}
		}
		out, err := json.Marshal(names)
		say(os.Stdout, "%s\n", out)
		return err
	case len(args) == 1 && args[0] == "report":
		return report(all)
	case len(args) != 1:
		return errors.New("usage: go run ./e2e [-push] <scenario> | list [<scenario>...] | report")
	case !slices.Contains(all, args[0]):
		return fmt.Errorf("no scenario %s; go run ./e2e list names them", args[0])
	case push && args[0] != "install":
		return errors.New("only install pushes")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Minute)
	defer cancel()
	return runScenario(ctx, args[0], push)
}

var tagLine = regexp.MustCompile(`(?m)^\s*@([a-z0-9-]+)\s*$`)

// scenarios are the tags of the feature files, but always.
func scenarios() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(featuresDir, "*.feature"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no %s/*.feature; run from the root of the checkout", featuresDir)
	}
	var names []string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, m := range tagLine.FindAllStringSubmatch(string(data), -1) {
			if m[1] != "always" {
				names = append(names, m[1])
			}
		}
	}
	slices.Sort(names)
	return names, nil
}

// outcome is a scenario's entry of report.yaml.
type outcome struct {
	Name   string `yaml:"name"`
	Result string `yaml:"result"`
	Error  string `yaml:"error,omitempty"`
	// Whether it started from the cache, or why not.
	Cache string `yaml:"cache,omitempty"`
}

// report prints the outcome of every scenario, from results/*/report.yaml.
func report(all []string) error {
	var outcomes []outcome
	for _, name := range all {
		o := outcome{Name: name, Result: "no report"}
		if data, err := os.ReadFile(filepath.Join("results", name, "report.yaml")); err == nil {
			if err := yaml.Unmarshal(data, &o); err != nil {
				return fmt.Errorf("the report of %s: %w", name, err)
			}
		}
		outcomes = append(outcomes, o)
	}
	out, err := yaml.Marshal(map[string]any{"scenarios": outcomes})
	say(os.Stdout, "%s", out)
	return err
}
