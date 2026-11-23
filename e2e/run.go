package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
	"go.yaml.in/yaml/v3"
)

// runScenario runs the scenario tagged @name, then always.feature once the
// cluster and the client are up, also after a failed line. With push it
// then saves the cluster as the cache.
func runScenario(ctx context.Context, name string, push bool) error {
	data, err := os.ReadFile(versions.File)
	if err != nil {
		return err
	}
	m, err := versions.Parse(data)
	if err != nil {
		return err
	}
	if err := m.Lab.Check(); err != nil {
		return err
	}
	res := &results{dir: filepath.Join("results", name)}
	if err := os.MkdirAll(res.dir, 0o755); err != nil {
		return err
	}
	if err := res.write("versions.yaml", string(data)); err != nil {
		return err
	}
	if err := installTools(ctx, m.Lab); err != nil {
		return err
	}
	s := &scenario{
		ctx: ctx, name: name, m: m, data: data, results: res, writes: true,
		marks: map[string]time.Time{}, alerts: map[string]bool{}, switched: map[string]string{}, restarts: map[string]int{},
	}
	suite := godog.TestSuite{
		Name: "e2e",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				return ctx, s.tearDown(err)
			})
			s.bind(sc)
		},
		Options: &godog.Options{
			Format: "progress,cucumber:" + res.path("cucumber.json"), Output: io.Discard, Strict: true, NoColors: true,
			DefaultContext: ctx, Paths: []string{featuresDir}, Tags: "@" + name + " && ~@always",
		},
	}
	if suite.Run() != 0 && s.failure == nil {
		s.failure = errors.New("a line did not bind to exactly one step")
	}
	if s.fault != nil {
		s.failure = errors.Join(s.failure, fmt.Errorf("keeping the run's files: %w", s.fault))
	}
	if s.failure == nil && push {
		s.failure = s.save(ctx)
	}
	o := outcome{Name: name, Result: "passed", Cache: s.cache}
	if s.failure != nil {
		o.Result, o.Error = "failed", s.failure.Error()
	}
	out, err := yaml.Marshal(o)
	if err := errors.Join(err, res.write("report.yaml", string(out))); err != nil {
		return errors.Join(err, s.failure)
	}
	if s.failure != nil {
		return s.failure
	}
	logf("PASSED: %s", name)
	return nil
}

// tearDown runs always.feature once the cluster and the client are up,
// after a failed line once its outages ended, and keeps the client's log,
// the nodes' logs, the timing table and the cluster's state. An error in
// keeping them goes to s.fault, so it never fails a passed scenario.
func (s *scenario) tearDown(err error) error {
	if s.clientRan {
		if err != nil {
			err = errors.Join(err, s.afterFailure())
		}
		logf("always.feature")
		err = errors.Join(err, s.runAlways())
	}
	s.failure = err
	// The files may take 50 s past the run's deadline.
	deadline, _ := s.ctx.Deadline()
	keep, cancel := context.WithDeadline(context.WithoutCancel(s.ctx), deadline.Add(50*time.Second))
	defer cancel()
	var fault error
	if s.clientRan {
		fault = s.stopClient(keep)
		log, logErr := rawClientLog(keep)
		fault = errors.Join(fault, logErr, s.results.write("client.log", log))
	}
	table := s.table()
	say(os.Stdout, "\n%s\n", table)
	fault = errors.Join(fault, s.results.write("timing.md", table))
	if s.lab != nil {
		// The node logs before the state, which a hung API server can make
		// take the rest of the time.
		fault = errors.Join(fault, s.lab.nodeLogs(keep, s.results))
		state := s.lab.state(keep)
		if err != nil {
			say(os.Stdout, "\n%s", state)
		}
		fault = errors.Join(fault, s.results.write("state.txt", state))
	}
	s.fault = fault
	return err
}

// table is the timing table of the scenario, in Markdown.
func (s *scenario) table() string {
	table := "### " + s.name + "\n\n| Line | Result | Took | Since | Measured |\n|---|---|---|---|---|\n"
	for _, r := range s.rows {
		table += fmt.Sprintf("| %s | %s | %s | %s | %s |\n", r.line, r.result, r.took, r.after, r.detail)
	}
	return table
}

// stateCommands show the state of the lab and the cluster.
var stateCommands = [][]string{
	{"docker", "ps", "-a"},
	{"docker", "logs", "--tail", "30", "acme"},
	{"docker", "logs", "--tail", "30", "s3"},
	{"docker", "logs", "--tail", "30", "registry"},
	{"docker", "logs", "--tail", "30", "client"},
	{"docker", "logs", "--tail", "30", "otel"},
	{"docker", "logs", "--tail", "30", "prometheus"},
	{"docker", "logs", "--tail", "30", "alertmanager"},
	{"kubectl", "get", "nodes", "-o", "wide"},
	{"kubectl", "get", "pods", "-A", "-o", "wide"},
	{"kubectl", "get", "clusters,backups,scheduledbackups,certificates,certificaterequests,orders,challenges", "-A"},
	{"kubectl", "get", "clusterissuers,issuers", "-A", "-o", "wide"},
	{"kubectl", "describe", "cluster", clusterName, "-n", namespace},
	{"kubectl", "get", "events", "-A", "--sort-by=.lastTimestamp"},
	{"kubectl", "logs", "-n", "cnpg-system", "-l", "app.kubernetes.io/name=cloudnative-pg", "--tail=100", "--prefix"},
	{"kubectl", "logs", "-n", "cnpg-system", "-l", "app.kubernetes.io/name=plugin-barman-cloud", "--tail=50", "--prefix"},
	{"kubectl", "logs", "-n", "registry", "-l", "app.kubernetes.io/name=registry", "--tail=30", "--prefix"},
	{"kubectl", "logs", "-n", "otel", "-l", "app.kubernetes.io/instance=otel-agent", "--tail=20", "--prefix"},
	// CNPG's jobs, such as a restore's: their pods end and show only as
	// Error otherwise.
	{"kubectl", "logs", "-n", namespace, "-l", "cnpg.io/jobRole", "--all-containers", "--tail=50", "--prefix", "--max-log-requests=20"},
}

// state is the state of the lab and the cluster, with the alerts' history.
func (l *lab) state(ctx context.Context) string {
	if kubeconfig == "" {
		// The install failed before the cluster was up.
		if err := l.writeKubeconfig(ctx); err != nil {
			logf("no kubeconfig: %v", err)
		}
	}
	state := "State:\n"
	for _, c := range stateCommands {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		cmd := exec.CommandContext(ctx, c[0], c[1:]...)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		out, err := cmd.CombinedOutput()
		cancel()
		state += fmt.Sprintf("\n$ %s\n%s", strings.Join(c, " "), out)
		if err != nil {
			state += err.Error() + "\n"
		}
	}
	return state + "\n" + l.mon.history(ctx, l.started)
}

// nodeLogs writes the log of k3s on each node to results/<node>.log.
func (l *lab) nodeLogs(ctx context.Context, res *results) error {
	var errs []error
	for i := range nodeCount {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		_, log, err := l.onNode(ctx, i, "journalctl", "--unit", "k3s", "--no-pager", "--output", "short-iso")
		cancel()
		if err != nil {
			log = err.Error()
			errs = append(errs, fmt.Errorf("the log of %s: %w", nodeName(i), err))
		}
		errs = append(errs, res.write(nodeName(i)+".log", log))
	}
	return errors.Join(errs...)
}

// results is where a scenario writes its files.
type results struct {
	dir string
}

func (r *results) path(name string) string { return filepath.Join(r.dir, name) }

func (r *results) write(name, content string) error {
	return os.WriteFile(r.path(name), []byte(content), 0o644)
}
