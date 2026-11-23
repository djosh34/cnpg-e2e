package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// runScenario runs the one scenario tagged @<name> of the feature files on
// the world, then always.feature, and writes its files to res. It fails
// when the scenario failed or could not run, and when the run could not
// keep its files.
func runScenario(ctx context.Context, w *world, name string, res *results) error {
	format := "progress,cucumber:" + res.path("cucumber.json") + ",junit:" + res.path("junit.xml")
	ran := 0
	var s *scenario
	suite := godog.TestSuite{
		Name: "e2e",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			s = &scenario{
				results: res, writes: true,
				marks: map[string]time.Time{}, limits: map[string]int{}, alerts: map[string]bool{},
				switched: map[string]string{}, restarts: map[string]int{},
			}
			sc.Before(func(ctx context.Context, sn *godog.Scenario) (context.Context, error) {
				ran++
				s.ctx = ctx
				return ctx, s.setUp(w, sn)
			})
			sc.After(func(ctx context.Context, sn *godog.Scenario, err error) (context.Context, error) {
				return ctx, s.tearDown(sn, err)
			})
			s.bind(sc)
		},
		Options: &godog.Options{
			Format: format, Output: io.Discard, Strict: true, NoColors: true, DefaultContext: ctx,
			Paths: []string{featuresDir},
			Tags:  "@" + name + " && ~@always",
		},
	}
	status := suite.Run()
	switch {
	case ran == 0:
		return fmt.Errorf("no scenario has the tag @%s", name)
	case ran > 1:
		return fmt.Errorf("%d scenarios have the tag @%s; a run runs one", ran, name)
	case s.fault != nil:
		return fmt.Errorf("the run could not keep its files: %w", s.fault)
	case status != 0:
		return fmt.Errorf("the scenario %s failed", name)
	}
	logf("PASSED: %s", name)
	return nil
}

// The feature files.
const featuresDir = "features"

// setUp checks the images on the nodes and names the instances.
func (s *scenario) setUp(w *world, sn *godog.Scenario) error {
	logf("scenario %q", sn.Name)
	s.w = w
	seen, err := nodeImagesPinned(s.ctx, w.m)
	s.images = append(s.images, seen...)
	if err != nil {
		return err
	}
	if s.roles, err = roles(s.ctx); err != nil {
		return err
	}
	logf("the primary is %s, replica 1 %s, replica 2 %s", s.roles["the primary"], s.roles["replica 1"], s.roles["replica 2"])
	s.started = time.Now()
	return nil
}

// tearDown runs always.feature after the scenario, passed or failed, once
// its cluster was built; after a failed line, it first ends the outages
// still in place. It keeps the client's log, the cluster's state and the
// nodes' logs, prints the timing table and, on a failure, the state. An
// error in keeping them is also in s.fault.
func (s *scenario) tearDown(sn *godog.Scenario, err error) error {
	if s.w != nil {
		if err != nil {
			err = errors.Join(err, s.afterFailure())
		}
		logf("always.feature")
		err = errors.Join(err, s.runAlways())
	}
	// The run's files have their own minute, the one TestScenario keeps
	// after the scenario's deadline, so a scenario that ran out of time
	// still leaves them.
	keep, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 50*time.Second)
	defer cancel()
	var fault error
	if s.clientRan {
		fault = s.stopClient(keep)
		log, logErr := rawClientLog(keep)
		fault = errors.Join(fault, logErr, s.results.write("client.log", log))
	}
	table := s.table(sn.Name)
	say(os.Stdout, "\n%s\n", table)
	state := dumpState(keep)
	if s.w != nil {
		state += "\n" + s.w.mon.history(keep, s.w.built)
	}
	if err != nil || fault != nil {
		say(os.Stdout, "\n%s", state)
	}
	s.images = append(s.images, imagesAtEnd(keep)...)
	if len(s.images) > 0 {
		slices.Sort(s.images)
		fault = errors.Join(fault, s.results.write("images.txt", strings.Join(slices.Compact(s.images), "\n")+"\n"))
	}
	s.fault = errors.Join(fault, s.results.write("timing.md", table), s.results.write("state.txt", state), nodeLogs(keep, s.results))
	return errors.Join(err, s.fault)
}

// nodeLogs writes the log of k3s on each node, from its journal, to
// <node>.log, so a node that never turns Ready can be traced. A log it
// cannot read has the error there instead, and fails the run's files.
func nodeLogs(ctx context.Context, res *results) error {
	var errs []error
	for i := range nodeCount {
		node := nodeName(i)
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		log, err := command(ctx, "docker", "exec", node, "journalctl", "--unit", "k3s", "--no-pager", "--output", "short-iso")
		cancel()
		if err != nil {
			log = err.Error()
			errs = append(errs, fmt.Errorf("the log of %s: %w", node, err))
		}
		errs = append(errs, res.write(node+".log", log+"\n"))
	}
	return errors.Join(errs...)
}

// table is the timing table of the scenario, in Markdown.
func (s *scenario) table(name string) string {
	table := "### " + name + "\n\n| Line | Result | Took | Since | Measured |\n|---|---|---|---|---|\n"
	for _, r := range s.rows {
		table += fmt.Sprintf("| %s | %s | %s | %s | %s |\n", r.line, r.result, r.took, r.after, r.detail)
	}
	return table
}

// What the state dump of a failed scenario shows.
var stateCommands = [][]string{
	{"docker", "ps", "-a"},
	{"docker", "logs", "--tail", "30", "dns"},
	{"docker", "logs", "--tail", "30", "acme"},
	{"docker", "logs", "--tail", "30", "s3"},
	{"docker", "logs", "--tail", "30", "registry"},
	{"docker", "logs", "--tail", "30", "client"},
	{"docker", "logs", "--tail", "30", "otel"},
	{"docker", "logs", "--tail", "30", "prometheus"},
	{"docker", "logs", "--tail", "30", "alertmanager"},
	{"docker", "logs", "--tail", "30", "grafana"},
	// The images on each node, also when always.feature stopped before it
	// checked them.
	{"docker", "exec", "k3s-1", "k3s", "crictl", "images", "--digests"},
	{"docker", "exec", "k3s-2", "k3s", "crictl", "images", "--digests"},
	{"docker", "exec", "k3s-3", "k3s", "crictl", "images", "--digests"},
	{"kubectl", "get", "nodes", "-o", "wide"},
	{"kubectl", "get", "pods", "-A", "-o", "wide"},
	{"kubectl", "get", "clusters,backups,certificates,certificaterequests,orders,challenges", "-A"},
	{"kubectl", "get", "clusterissuers,issuers", "-A", "-o", "wide"},
	{"kubectl", "get", "services", "-A"},
	{"kubectl", "describe", "cluster", clusterName, "-n", namespace},
	{"kubectl", "get", "events", "-A", "--sort-by=.lastTimestamp"},
	{"kubectl", "logs", "-n", "cnpg-system", "-l", "app.kubernetes.io/name=cloudnative-pg", "--tail=100", "--prefix"},
	{"kubectl", "logs", "-n", "otel", "-l", "app.kubernetes.io/instance=otel-agent", "--tail=20", "--prefix"},
	{"kubectl", "logs", "-n", "cnpg-system", "-l", "app.kubernetes.io/name=plugin-barman-cloud", "--tail=50", "--prefix"},
	// CNPG's jobs, such as a restore's full-recovery: their pods end and
	// show only as Error otherwise.
	{"kubectl", "logs", "-n", namespace, "-l", "cnpg.io/jobRole", "--all-containers", "--tail=50", "--prefix", "--max-log-requests=20"},
}

func dumpState(ctx context.Context) string {
	state := "State of the cluster:\n"
	for _, c := range stateCommands {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		cmd := exec.CommandContext(ctx, c[0], c[1:]...)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		// docker logs prints the container's stderr on its own stderr.
		out, err := cmd.CombinedOutput()
		cancel()
		state += fmt.Sprintf("\n$ %s\n%s", strings.Join(c, " "), out)
		if err != nil {
			state += err.Error() + "\n"
		}
	}
	return state
}

// results is where a scenario writes its files: results/<scenario>/ of
// the checkout, which the e2e workflow uploads.
type results struct {
	dir string
}

func newResults(scenario string) (*results, error) {
	r := &results{dir: filepath.Join("..", "results", scenario)}
	return r, os.MkdirAll(r.dir, 0o755)
}

func (r *results) path(name string) string { return filepath.Join(r.dir, name) }

func (r *results) write(name, content string) error {
	return os.WriteFile(r.path(name), []byte(content), 0o644)
}
