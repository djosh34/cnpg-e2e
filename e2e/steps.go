package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"
	"github.com/djosh34/cnpg-e2e/e2e/versions"
)

// The line shapes. Most lines are "<thing> is <word>": the step looks up
// thing and word in the vocabulary (vocab.go), which says what to do.
//
//	empty nodes | empty VMs | a running cluster
//	<thing> is|are <word> [< N s [after the <event>] | for N s]
//	<thing> has|have been <word> since the <event>
//	<thing> starts|stops
//	N s pass [after the <event>]
//	<ALERT> [<severity>] fired|may fire|fires|resolves|has not fired for <who> [< N s [after the <event>]]
//	Alertmanager gets|sends|mutes [the resolved] <ALERT> [<severity>] for <who> [< N s [after the <event>]]
//	no writes
//
// A check waits up to "< N s" from the line above or after the event, or
// else up to its word's own limit. "for N s" watches it for N s, and
// "since the <event>" checks the whole time from the event.
const (
	within    = ` < (\d+) s(?: after the (\w+))?`
	setupLine = `^(empty nodes|empty VMs|a running cluster)$`
	isLine    = `^(.+?) (?:is|are) (.+?)(?:` + within + `| for (\d+) s)?$`
	sinceLine = `^(.+?) (?:has|have) been (.+) since the (\w+)$`
	verbLine  = `^(.+?) (starts?|stops)$`
	passLine  = `^(\d+) s pass(?: after the (\w+))?$`
	alertLine = `^([A-Z]\w+)(?: (warning|critical))? (fired|may fire|fires|resolves|has not fired) for (.+?)(?:` + within + `)?$`
	amLine    = `^Alertmanager (gets|sends|mutes) (the resolved )?([A-Z]\w+)(?: (warning|critical))? for (.+?)(?:` + within + `)?$`
	quietLine = `^no writes$`
)

func (s *scenario) bind(sc *godog.ScenarioContext) {
	sc.StepContext().Before(s.beforeStep)
	sc.StepContext().After(s.afterStep)

	sc.Step(setupLine, s.setup)
	sc.Step(isLine, s.is)
	sc.Step(sinceLine, s.since)
	sc.Step(verbLine, s.verb)
	sc.Step(passLine, s.pass)
	sc.Step(alertLine, s.alert)
	sc.Step(amLine, s.alertmanager)
	sc.Step(quietLine, s.noWrites)
}

// scenario is the state of the run's one scenario.
type scenario struct {
	ctx     context.Context
	name    string
	m       *versions.Manifest // the run's versions file
	data    []byte             // and its content
	before  *versions.Manifest // main's, which the upgrade installs first
	results *results
	lab     *lab
	cache   string // whether the cluster started from the cache, or why not
	up      bool   // the cluster and its database are up
	failure error  // why the scenario failed
	fault   error  // why tearDown could not keep the run's files

	kind  messages.PickleStepType // Given, When or Then of the line running
	roles map[string]string       // the instances by role, from the first line that names one
	marks map[string]time.Time    // when each named event happened
	last  string                  // the last named event

	clientRan   bool      // the client was started
	clientEnded time.Time // when it was stopped
	writes      bool      // false after "Given no writes"

	// The outages in place, which endOutages ends after a failed line.
	down     []string // the nodes killed and not started again
	downPods []string // the instances on them
	fenced   []string // the instances fenced and not unfenced
	paused   []string // the instances whose replay is paused
	cut      []string // the instances cut off and not reconnected

	switched map[string]string // the WAL file each instance switched away from last
	restarts map[string]int    // the restart count of an instance's postgres container before it was killed
	ident    string            // the pg_ident lines every instance had at the first line about them
	store    string            // how the store is broken: stopped, paused, or ""
	commit   *background       // the commit "a commit is started" started
	ballast  string            // the node whose data disk has the ballast file
	held     *held             // the sessions held on an instance

	started    time.Time       // when the cluster was up
	alerts     map[string]bool // the alerts the scenario names, "<alertname> <pod>": true when they must fire, false when they may
	writeLimit int             // the limit of writing the scenario set
	primaryOut bool            // the primary changed or stopped

	stepStart time.Time
	holding   time.Time // when the "for N s" line running started
	detail    string    // what the line running measured, for the timing table
	rows      []row
}

// One line of the timing table.
type row struct {
	line, result, took, after, detail string
}

// beforeStep starts the client at the first When or Then line once the
// cluster is up, with its writes unless the scenario said "Given no writes".
func (s *scenario) beforeStep(ctx context.Context, st *godog.Step) (context.Context, error) {
	s.kind, s.stepStart, s.detail = st.Type, time.Now(), ""
	logf("%s %s", keyword(st.Type), st.Text)
	if st.Type != messages.PickleStepType_CONTEXT && s.up && !s.clientRan {
		logf("starting the client, writes %t", s.writes)
		s.clientRan = true
		if err := s.startClient(ctx); err != nil {
			return ctx, fmt.Errorf("the client: %w", err)
		}
	}
	return ctx, nil
}

// afterStep adds the line to the timing table, with its time after the
// last named event.
func (s *scenario) afterStep(ctx context.Context, st *godog.Step, status godog.StepResultStatus, err error) (context.Context, error) {
	r := row{line: keyword(st.Type) + " " + st.Text, result: status.String(), took: seconds(time.Since(s.stepStart)), detail: s.detail}
	if s.last != "" {
		r.after = seconds(time.Since(s.marks[s.last])) + " after the " + s.last
	}
	s.rows = append(s.rows, r)
	switch {
	case err != nil:
		logf("  %s in %s: %v", r.result, r.took, err)
	case r.detail != "":
		logf("  %s in %s: %s", r.result, r.took, r.detail)
	default:
		logf("  %s in %s", r.result, r.took)
	}
	return ctx, nil
}

func keyword(t messages.PickleStepType) string {
	switch t {
	case messages.PickleStepType_CONTEXT:
		return "Given"
	case messages.PickleStepType_ACTION:
		return "When"
	case messages.PickleStepType_OUTCOME:
		return "Then"
	}
	return "*"
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.1f s", d.Seconds()) }

// setup: what a scenario starts from, its first line.
func (s *scenario) setup(from string) error {
	switch {
	case s.kind != messages.PickleStepType_CONTEXT || s.lab != nil:
		return fmt.Errorf("%s must be the first line, a Given", from)
	case from == "a running cluster":
		return s.runningCluster()
	default:
		return s.emptyMachines(from == "empty VMs")
	}
}

// is: "<thing> is <word>", with "< N s", "< N s after the <event>" or
// "for N s".
func (s *scenario) is(thing, word, n, after, forN string) error {
	key, a, err := s.lookup(thing, word)
	if err != nil {
		return err
	}
	w := vocabulary[key]
	switch {
	case w.since != nil:
		return fmt.Errorf("%s takes only \"has been ... since the <event>\"", key)
	case w.event != nil:
		if n != "" || forN != "" {
			return fmt.Errorf("an event takes no limit: %s", key)
		}
		return s.event(key, w, a)
	case s.kind == messages.PickleStepType_ACTION:
		return fmt.Errorf("a check in a When line: %s", key)
	case w.longest != nil && after != "":
		return s.measureBack(w, n, after)
	case w.longest != nil:
		return s.measure(key, w, n, forN)
	}
	check := func(ctx context.Context) error { return w.state(ctx, s, a) }
	if forN != "" {
		return s.holdsFor(forN, check)
	}
	if n == "" && w.limit > 0 {
		n = strconv.Itoa(w.limit)
	}
	return s.within(n, after, check)
}

// since: "<thing> has been <word> since the <event>".
func (s *scenario) since(thing, word, event string) error {
	key, a, err := s.lookup(thing, word)
	if err != nil {
		return err
	}
	w := vocabulary[key]
	if w.since == nil {
		return fmt.Errorf("%s takes no \"since\"", key)
	}
	if s.kind == messages.PickleStepType_ACTION {
		return fmt.Errorf("a check in a When line: %s", key)
	}
	from, ok := s.marks[event]
	if !ok {
		return fmt.Errorf("no %s yet", event)
	}
	return w.since(s.ctx, s, a, from)
}

// alert: "<ALERT> fires for <who> < N s" and the other alert words. fires
// and resolves wait up to N s, or check now; fired and has not fired check
// the whole scenario. always.feature's "the alerts are healthy" fails on an
// alert the scenario does not say fired, fires or may fire.
func (s *scenario) alert(name, severity, verb, who, n, after string) error {
	if s.kind == messages.PickleStepType_ACTION {
		return errors.New("an alert check in a When line")
	}
	labels, err := s.alertLabels(name, severity, who)
	if err != nil {
		return err
	}
	key := alertKey(name, labels["pod"])
	var check func(context.Context) error
	switch verb {
	case "may fire":
		if severity != "" || n != "" {
			return errors.New("may fire takes no severity and no limit")
		}
		if !s.alerts[key] {
			s.alerts[key] = false
		}
		return nil
	case "fires":
		s.alerts[key] = true
		check = s.alertIs(labels, true, false)
	case "resolves":
		check = s.alertIs(labels, false, false)
	case "fired":
		s.alerts[key] = true
		check = s.alertIs(labels, true, true)
	case "has not fired":
		check = s.alertIs(labels, false, true)
	}
	return s.within(n, after, check)
}

// alertLabels are the labels of the alert a line names: of an instance by
// its role, of the cluster, or of the server or replication certificate.
func (s *scenario) alertLabels(name, severity, who string) (map[string]string, error) {
	labels := map[string]string{"alertname": name, "pod": ""}
	if severity != "" {
		labels["severity"] = severity
	}
	switch who {
	case "the cluster":
		labels["cluster"] = clusterName
	case "the server certificate", "the replication certificate":
		labels["cluster"] = clusterName
		labels["name"] = clusterName + "-" + strings.Fields(who)[1]
	default:
		pod, err := s.role(who)
		if err == nil && pod == anotherInstance {
			err = errors.New("an alert names one instance, the cluster or a certificate")
		}
		if err != nil {
			return nil, err
		}
		labels["pod"] = pod
	}
	return labels, nil
}

// alertIs checks that an alert with the labels fires now, or fired in the
// scenario, or not.
func (s *scenario) alertIs(labels map[string]string, want, inScenario bool) func(context.Context) error {
	mon := s.lab.mon
	return func(ctx context.Context) error {
		var from time.Time
		what := "fires"
		if inScenario {
			from, what = s.started, "fired in the scenario"
		}
		got, err := mon.alertFires(ctx, labels, from)
		if err != nil || got == want {
			return err
		}
		all, err := mon.firing(ctx)
		if inScenario {
			all, err = mon.fired(ctx, s.started)
		}
		return errors.Join(fmt.Errorf("%s %s: %t; the alerts: %s", selector(labels), what, got,
			strings.Join(slices.Sorted(maps.Keys(all)), ", ")), err)
	}
}

// alertmanager: "Alertmanager gets <ALERT> for <role>" sends Alertmanager
// that alert as the rule evaluator would; "sends" waits until the receiver
// got it, firing or resolved, and "mutes" until Alertmanager holds it
// suppressed.
func (s *scenario) alertmanager(verb, resolved, name, severity, who, n, after string) error {
	labels, err := s.alertLabels(name, severity, who)
	if err != nil {
		return err
	}
	if resolved != "" && verb != "sends" {
		return errors.New("only sends takes the resolved")
	}
	mon := s.lab.mon
	if verb == "gets" {
		if s.kind == messages.PickleStepType_OUTCOME || n != "" || severity != "" || labels["pod"] == "" {
			return errors.New("gets: an event of one instance, with no limit and no severity")
		}
		return mon.amSend(s.ctx, name, labels["pod"])
	}
	if s.kind == messages.PickleStepType_ACTION {
		return errors.New("an Alertmanager check in a When line")
	}
	status := "firing"
	if resolved != "" {
		status = "resolved"
	}
	check := func(ctx context.Context) error { return mon.sent(labels, status, s.started) }
	if verb == "mutes" {
		check = func(ctx context.Context) error {
			state, err := mon.amState(ctx, labels)
			if err == nil && state != "suppressed" {
				err = fmt.Errorf("the state of %s in Alertmanager: %q", selector(labels), state)
			}
			return err
		}
	}
	return s.within(n, after, check)
}

// verb: "<thing> starts" or "stops".
func (s *scenario) verb(thing, verb string) error { return s.is(thing, verb, "", "", "") }

// pass: "N s pass", from the line above or after an event.
func (s *scenario) pass(n, after string) error {
	if s.kind == messages.PickleStepType_OUTCOME {
		return errors.New("a wait in a Then line")
	}
	until, err := s.deadline(n, after)
	if err != nil {
		return err
	}
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-time.After(time.Until(until)):
		return nil
	}
}

// noWrites: "Given no writes": the client only reads.
func (s *scenario) noWrites() error {
	if s.clientRan {
		return errors.New("no writes must come before the first When")
	}
	s.writes = false
	return nil
}

// event does an event and marks its time, for "after the <event>" and
// "since the <event>": when it is done.
func (s *scenario) event(key string, w word, a args) error {
	if s.kind == messages.PickleStepType_OUTCOME {
		return fmt.Errorf("an event in a Then line: %s", key)
	}
	if err := w.event(s.ctx, s, a); err != nil {
		return err
	}
	if w.mark != "" {
		s.marks[w.mark], s.last = time.Now(), w.mark
	}
	return nil
}

// measure checks the longest time without a read, or without an
// acknowledged write, over the scenario. A scenario's own "writing is
// healthy < N s" is the limit of writing in always.feature too. Where the
// primary changed or stopped, reading takes the limit of writing: the
// client reads on rw only.
func (s *scenario) measure(key string, w word, n, forN string) error {
	if forN != "" {
		return fmt.Errorf("%s takes only a limit, < N s", key)
	}
	limit := w.limit
	if n != "" {
		var err error
		if limit, err = strconv.Atoi(n); err != nil {
			return err
		}
	}
	switch key {
	case "writing | healthy":
		if n != "" {
			s.writeLimit = limit
		} else if s.writeLimit > 0 {
			limit = s.writeLimit
		}
	case "reading | healthy":
		if s.primaryOut {
			limit = vocabulary["writing | healthy"].limit
			if s.writeLimit > 0 {
				limit = s.writeLimit
			}
		}
	}
	got, err := w.longest(s.ctx, s)
	if err != nil {
		return err
	}
	s.detail = "longest " + seconds(got)
	if got >= time.Duration(limit)*time.Second {
		return fmt.Errorf("%s: the longest time without one was %s, not < %d s", key, seconds(got), limit)
	}
	return nil
}

// measureBack waits until the measure is back after the event, by N s after
// it, by when it came back, not when the e2e saw it.
func (s *scenario) measureBack(w word, n, after string) error {
	deadline, err := s.deadline(n, after)
	if err != nil {
		return err
	}
	from := s.marks[after]
	var back time.Time
	err = poll(s.ctx, deadline, func(ctx context.Context) error {
		var err error
		back, err = w.back(ctx, s, from)
		return err
	})
	if err != nil && !back.IsZero() && !back.After(deadline) {
		return nil
	}
	return err
}

func (s *scenario) holdsFor(n string, check func(context.Context) error) error {
	secs, err := strconv.Atoi(n)
	if err != nil {
		return err
	}
	s.holding = time.Now()
	defer func() { s.holding = time.Time{} }()
	end := s.holding.Add(time.Duration(secs) * time.Second)
	for {
		if err := check(s.ctx); err != nil {
			return fmt.Errorf("it stopped holding %s before the end: %w", seconds(time.Until(end)), err)
		}
		if time.Now().After(end) {
			return nil
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// within checks now without a limit, or waits until the check holds, by N s
// from now or N s after a named event.
func (s *scenario) within(n, after string, check func(context.Context) error) error {
	if n == "" {
		return check(s.ctx)
	}
	deadline, err := s.deadline(n, after)
	if err != nil {
		return err
	}
	return poll(s.ctx, deadline, check)
}

// deadline is N s from now, or N s after a named event.
func (s *scenario) deadline(n, after string) (time.Time, error) {
	secs, err := strconv.Atoi(n)
	if err != nil {
		return time.Time{}, err
	}
	from := time.Now()
	if after != "" {
		var ok bool
		if from, ok = s.marks[after]; !ok {
			return time.Time{}, fmt.Errorf("no %s yet", after)
		}
	}
	return from.Add(time.Duration(secs) * time.Second), nil
}

// stops marks the primary as stopped when one of the pods is the primary
// now: the one that became primary last, else the first.
func (s *scenario) stops(pods ...string) {
	primary, ok := s.roles["the new primary"]
	if !ok {
		primary = s.roles["the primary"]
	}
	if slices.Contains(pods, primary) {
		s.primaryOut = true
	}
}

// The roles a line can name. "another instance" is any instance but the
// primary; "the new primary" is the instance that became primary last; "a
// replica" is one other than that.
var roleNames = []string{"the new primary", "the primary", "replica 1", "replica 2", "another instance", "a replica"}

const anotherInstance = ""

// args are what lookup took out of a line: the pods of its roles and its
// numbers, in order.
type args struct {
	pods []string
	ns   []int
}

// lookup turns thing and word into a vocabulary key, with each role as
// <role> and each number as <n>. Digits right after a letter are part of a
// word, as in k3s.
func (s *scenario) lookup(thing, word string) (string, args, error) {
	var a args
	key := ""
	text := thing + " | " + word
next:
	for i := 0; i < len(text); {
		for _, name := range roleNames {
			if strings.HasPrefix(text[i:], name) {
				pod, err := s.role(name)
				if err != nil {
					return "", a, err
				}
				a.pods = append(a.pods, pod)
				key += "<role>"
				i += len(name)
				continue next
			}
		}
		if digits := leadingDigits(text[i:]); digits != "" && (i == 0 || !isLetter(text[i-1])) {
			n, err := strconv.Atoi(digits)
			if err != nil {
				return "", a, err
			}
			a.ns = append(a.ns, n)
			key += "<n>"
			i += len(digits)
			continue
		}
		key += text[i : i+1]
		i++
	}
	if _, ok := vocabulary[key]; !ok {
		return "", a, fmt.Errorf("not in the vocabulary: %q", key)
	}
	return key, a, nil
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func leadingDigits(text string) string {
	end := 0
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	return text[:end]
}

// newPrimary names the instance that became primary: the new primary, and a
// replica one of the others.
func (s *scenario) newPrimary(pod string) {
	s.roles["the new primary"] = pod
	for _, role := range []string{"the primary", "replica 1", "replica 2"} {
		if other := s.roles[role]; other != pod {
			s.roles["a replica"] = other
			return
		}
	}
}

// role is the pod of a role. The first line that names one names the
// instances: the primary, and the replicas by pod name.
func (s *scenario) role(name string) (string, error) {
	if name == "another instance" {
		return anotherInstance, nil
	}
	if s.roles == nil && s.up {
		primary, err := clusterStatus(s.ctx, clusterName, "currentPrimary")
		if err != nil {
			return "", err
		}
		pods, err := instances(s.ctx, clusterName)
		if err != nil {
			return "", err
		}
		replicas := slices.DeleteFunc(pods, func(p string) bool { return p == primary })
		if len(replicas) != nodeCount-1 {
			return "", fmt.Errorf("instances besides the primary %s: %v", primary, replicas)
		}
		s.roles = map[string]string{"the primary": primary, "replica 1": replicas[0], "replica 2": replicas[1], "a replica": replicas[0]}
		logf("the primary is %s, replica 1 %s, replica 2 %s", primary, replicas[0], replicas[1])
	}
	pod, ok := s.roles[name]
	if !ok {
		return "", fmt.Errorf("no %s yet", name)
	}
	return pod, nil
}

// runAlways runs always.feature with the same steps and state. Its first
// failing line fails the scenario.
func (s *scenario) runAlways() error {
	var failed error
	suite := godog.TestSuite{
		Name: "always",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			s.bind(sc)
			sc.StepContext().After(func(ctx context.Context, st *godog.Step, _ godog.StepResultStatus, err error) (context.Context, error) {
				if err != nil && failed == nil {
					failed = fmt.Errorf("always.feature: %s: %w", st.Text, err)
				}
				return ctx, nil
			})
		},
		Options: &godog.Options{
			Format: "progress,cucumber:" + s.results.path("always.cucumber.json"), Output: io.Discard, Strict: true, NoColors: true,
			DefaultContext: s.ctx, Paths: []string{filepath.Join(featuresDir, "always.feature")},
		},
	}
	if suite.Run() != 0 && failed == nil {
		failed = errors.New("always.feature: a line did not bind to exactly one step")
	}
	return failed
}
