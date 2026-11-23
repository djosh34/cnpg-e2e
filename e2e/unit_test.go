package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	gherkin "github.com/cucumber/gherkin/go/v42"
	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"
)

func TestParseLog(t *testing.T) {
	log := parseLog(strings.Join([]string{
		"1000 1100 read k3s-1.example.test:5432 ok 10.42.0.5 recovery=false",
		"1000 1200 write k3s-1.example.test:5432 ok 17 10.42.0.5",
		"client: creating e2e_writes: no route",
		"1250 1300 write k3s-2.example.test:5433 fail ERROR: cannot execute INSERT in a read-only transaction",
		"1300 1310 connect k3s-3.example.test:5432 ok ",
		// Longer than bufio.Scanner's default limit of 64 KiB.
		"1400 1410 read - fail " + strings.Repeat("x", 70_000),
		"1500 1600 writes - stopped ",
	}, "\n"))
	if len(log.lines) != 6 {
		t.Fatalf("lines: got %d, want 6", len(log.lines))
	}
	if n, server := log.lines[1].write(); n != "17" || server != "10.42.0.5" {
		t.Errorf("write: got %q %q, want 17 10.42.0.5", n, server)
	}
	if !log.connected("k3s-3.example.test:5432") || log.connected("k3s-1.example.test:5432") {
		t.Error("connected: want only k3s-3.example.test:5432")
	}
	if got := log.acknowledged(); len(got) != 1 || got[0] != "17" {
		t.Errorf("acknowledged: got %v, want [17]", got)
	}
	if first, ok := log.first("write"); !ok || first.UnixMilli() != 1200 {
		t.Errorf("first write: got %v %v, want 1200", first.UnixMilli(), ok)
	}
	if stopped, ok := log.writesStopped(); !ok || stopped.UnixMilli() != 1500 {
		t.Errorf("writes stopped: got %v %v, want 1500", stopped.UnixMilli(), ok)
	}
	if log.tail != "client: creating e2e_writes: no route" {
		t.Errorf("tail: got %q", log.tail)
	}
}

func TestLongestGap(t *testing.T) {
	ms := func(n int64) time.Time { return time.UnixMilli(n) }
	log := parseLog(strings.Join([]string{
		"0 1000 read h:5432 ok a",
		// Attempts run side by side: this one ended last but is logged first.
		"2000 9000 read h:5432 ok a",
		"3000 3500 read h:5432 ok a",
		"4000 4100 read - fail timeout",
		"4000 4200 write h:5432 ok 1 a",
	}, "\n"))
	tests := []struct {
		name     string
		from, to int64
		want     time.Duration
	}{
		{"between reads", 1000, 9500, 5500 * time.Millisecond},
		{"to the end", 1000, 20000, 11000 * time.Millisecond},
		{"from the start", 0, 1000, time.Second},
		{"nothing in the window", 10000, 12000, 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := log.longestGap("read", ms(tt.from), ms(tt.to)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// An event a fraction of a ms after a line started comes after the line.
func TestStartedBefore(t *testing.T) {
	line := logLine{start: 1000}
	if !line.startedBefore(time.UnixMilli(1000).Add(500 * time.Microsecond)) {
		t.Error("a line at 1000 ms did not start before 1000.5 ms")
	}
	if line.startedBefore(time.UnixMilli(1000)) {
		t.Error("a line at 1000 ms started before 1000 ms")
	}
}

func TestLookup(t *testing.T) {
	s := &scenario{roles: map[string]string{"the primary": "pg-1", "replica 1": "pg-2", "replica 2": "pg-3"}}
	key, a, err := s.lookup("replica 1", "promoted")
	if err != nil || key != "<role> | promoted" || len(a.pods) != 1 || a.pods[0] != "pg-2" {
		t.Errorf("replica 1 is promoted: got %q %v %v", key, a.pods, err)
	}
	key, a, err = s.lookup("another instance", "primary")
	if err != nil || key != "<role> | primary" || a.pods[0] != anotherInstance {
		t.Errorf("another instance is primary: got %q %v %v", key, a.pods, err)
	}
	if _, _, err := s.lookup("replica 1", "kiled"); err == nil {
		t.Error("a word not in the vocabulary: no error")
	}
	if _, _, err := s.lookup("the new primary", "primary"); err == nil {
		t.Error("the new primary before any change: no error")
	}
	key, a, err = s.lookup("1229 MB of WAL", "written on the primary")
	if err != nil || key != "<n> MB of WAL | written on <role>" || len(a.ns) != 1 || a.ns[0] != 1229 || a.pods[0] != "pg-1" {
		t.Errorf("1229 MB of WAL is written on the primary: got %q %v %v %v", key, a.ns, a.pods, err)
	}
	key, a, err = s.lookup("k3s", "restarted on node 2")
	if err != nil || key != "k3s | restarted on node <n>" || len(a.ns) != 1 || a.ns[0] != 2 {
		t.Errorf("k3s is restarted on node 2: got %q %v %v", key, a.ns, err)
	}
}

func TestNewPrimary(t *testing.T) {
	s := &scenario{roles: map[string]string{"the primary": "pg-1", "replica 1": "pg-2", "replica 2": "pg-3", "a replica": "pg-2"}}
	s.newPrimary("pg-2")
	if s.roles["the new primary"] != "pg-2" || s.roles["a replica"] != "pg-1" {
		t.Errorf("after pg-2 became primary: %v", s.roles)
	}
	s.newPrimary("pg-1")
	if s.roles["the new primary"] != "pg-1" || s.roles["a replica"] != "pg-2" {
		t.Errorf("after pg-1 became primary: %v", s.roles)
	}
}

// Where the primary changed or stopped, reading takes the limit of writing:
// the scenario's own, else always.feature's, even before always.feature's
// writing line runs. Elsewhere it keeps its own.
func TestReadingLimit(t *testing.T) {
	gap := func(context.Context, *scenario) (time.Duration, error) { return 100 * time.Second, nil }
	reading := word{longest: gap, writingLimit: true}
	writing := word{longest: gap, scenarioLimit: true}
	run := func(primaryOut bool, own string) error {
		s := &scenario{limits: map[string]int{}, primaryOut: primaryOut}
		if own != "" && s.measure("writing | healthy", writing, own, "", "") == nil {
			t.Fatalf("writing < %s s passed a gap of 100 s", own)
		}
		s.inAlways = true
		always := &godog.Scenario{Steps: []*messages.PickleStep{
			{Text: "reading is healthy < 5 s"}, {Text: "writing is healthy < 180 s"},
		}}
		if err := s.keepWritingLimit(always); err != nil {
			t.Fatal(err)
		}
		return s.measure("reading | healthy", reading, "5", "", "")
	}
	if run(false, "") == nil {
		t.Error("without a change of primary: reading < 5 s passed a gap of 100 s")
	}
	if err := run(true, ""); err != nil {
		t.Errorf("with a change of primary: reading took the limit of 180 s and failed: %v", err)
	}
	if run(true, "30") == nil {
		t.Error("with a change of primary and writing < 30 s: reading passed a gap of 100 s")
	}
}

// Every line of the feature files binds to exactly one line shape, every
// "is", "has been" and verb line is in the vocabulary, and every alert line
// names a role, the cluster or a certificate. Each scenario has its own tag.
func TestFeaturesUseTheVocabulary(t *testing.T) {
	shapes := map[string]*regexp.Regexp{
		"is": regexp.MustCompile(isLine), "since": regexp.MustCompile(sinceLine), "verb": regexp.MustCompile(verbLine),
		"pass": regexp.MustCompile(passLine), "alert": regexp.MustCompile(alertLine),
		"alertmanager": regexp.MustCompile(amLine), "quiet": regexp.MustCompile(quietLine),
	}
	files, err := filepath.Glob("features/*.feature")
	if err != nil || len(files) == 0 {
		t.Fatalf("no feature files: %v", err)
	}
	s := &scenario{roles: map[string]string{
		"the primary": "pg-1", "replica 1": "pg-2", "replica 2": "pg-3", "the new primary": "pg-2", "a replica": "pg-1",
	}}
	id := 0
	newID := func() string { id++; return strconv.Itoa(id) }
	var tags []string
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := gherkin.ParseGherkinDocument(f, newID)
		if err := errors.Join(err, f.Close()); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, pickle := range gherkin.Pickles(*doc, file, newID) {
			for _, tag := range pickle.Tags {
				if tag.Name != "@always" {
					tags = append(tags, strings.TrimPrefix(tag.Name, "@"))
				}
			}
			for _, step := range pickle.Steps {
				var matched []string
				for name, re := range shapes {
					if m := re.FindStringSubmatch(step.Text); m != nil {
						matched = append(matched, name)
						var err error
						switch name {
						case "is", "verb", "since":
							_, _, err = s.lookup(m[1], m[2])
						case "alert":
							_, err = s.alertLabels(m[1], m[2], m[4])
						case "alertmanager":
							_, err = s.alertLabels(m[3], m[4], m[5])
						}
						if err != nil {
							t.Errorf("%s: %q: %v", file, step.Text, err)
						}
					}
				}
				if len(matched) != 1 {
					t.Errorf("%s: %q matches %v, want one line shape", file, step.Text, matched)
				}
				if step.Type == messages.PickleStepType_UNKNOWN {
					t.Errorf("%s: %q has no keyword", file, step.Text)
				}
			}
		}
	}
	slices.Sort(tags)
	for i := 1; i < len(tags); i++ {
		if tags[i] == tags[i-1] {
			t.Errorf("the tag @%s is on more than one scenario", tags[i])
		}
	}
}

// The dashboard decodes as the e2e test reads it, and has the two stat
// panels its lines read: Replica lag, with a query it shows in red, and
// Instances, with one query.
func TestDashboard(t *testing.T) {
	data, err := os.ReadFile("images/grafana/dashboard.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Panels []panel `json:"panels"`
	}
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatal(err)
	}
	stats := map[string]panel{}
	for _, p := range dashboard.Panels {
		if _, seen := stats[p.Title]; p.Type == "stat" && !seen {
			stats[p.Title] = p
		}
	}
	if red, err := stats["Replica lag"].redQueries(); err != nil || len(red) != 1 {
		t.Errorf("Replica lag shows %v in red: %v", red, err)
	}
	if n := len(stats["Instances"].Targets); n != 1 {
		t.Errorf("Instances has %d queries", n)
	}
}

// A pod missing from k evaluations in a row of a panel every 15 s, also at
// the start or the end, was gone for k steps.
func TestPanelGap(t *testing.T) {
	shown := func(ts ...int64) map[int64]bool {
		m := map[int64]bool{}
		for _, t := range ts {
			m[t] = true
		}
		return m
	}
	tests := []struct {
		name  string
		shown map[int64]bool
		want  time.Duration
	}{
		{"always", shown(0, 15, 30, 45, 60), 0},
		{"one missing", shown(0, 15, 45, 60), 15 * time.Second},
		{"three missing", shown(0, 60), 45 * time.Second},
		{"the first three missing", shown(45, 60), 45 * time.Second},
		{"the last two missing", shown(0, 15, 30), 30 * time.Second},
		{"none shown", shown(), 75 * time.Second},
	}
	for _, tt := range tests {
		if got := longestGap(tt.shown, 0, 60, 15); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

// An alert line's labels: the pod of a role, the cluster, or a
// Certificate's name, with the severity when the line gives one. A
// notification matches them when it has each, and lacks those that are "".
func TestAlertLabels(t *testing.T) {
	s := &scenario{roles: map[string]string{"the primary": "pg-1", "replica 1": "pg-2"}}
	tests := []struct {
		severity, who string
		want          map[string]string
	}{
		{"", "replica 1", map[string]string{"alertname": "A", "pod": "pg-2"}},
		{"critical", "the cluster", map[string]string{"alertname": "A", "pod": "", "cluster": "pg", "severity": "critical"}},
		{"warning", "the server certificate", map[string]string{"alertname": "A", "pod": "", "cluster": "pg", "name": "pg-server", "severity": "warning"}},
	}
	for _, tt := range tests {
		got, err := s.alertLabels("A", tt.severity, tt.who)
		if err != nil || !maps.Equal(got, tt.want) {
			t.Errorf("%s: got %v %v, want %v", tt.who, got, err, tt.want)
		}
	}
	for _, who := range []string{"another instance", "the client certificate", "replica 3"} {
		if _, err := s.alertLabels("A", "", who); err == nil {
			t.Errorf("%s: no error", who)
		}
	}
	labels := map[string]string{"alertname": "A", "pod": "", "cluster": "pg"}
	if !hasLabels(map[string]string{"alertname": "A", "cluster": "pg", "severity": "critical"}, labels) {
		t.Error("an alert of the cluster did not match")
	}
	if hasLabels(map[string]string{"alertname": "A", "cluster": "pg", "pod": "pg-1"}, labels) {
		t.Error("an alert of an instance matched the cluster")
	}
	if got := selector(labels); got != `{alertname="A", cluster="pg", pod=""}` {
		t.Errorf("selector: %s", got)
	}
}
