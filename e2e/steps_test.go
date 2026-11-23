package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	gherkin "github.com/cucumber/gherkin/go/v42"
	messages "github.com/cucumber/messages/go/v34"
)

// Every scenario starts with what it starts from, and every line binds to
// exactly one line shape and names a word of the vocabulary.
func TestFeatures(t *testing.T) {
	shapes := map[string]*regexp.Regexp{
		"setup": regexp.MustCompile(setupLine), "is": regexp.MustCompile(isLine), "since": regexp.MustCompile(sinceLine),
		"verb": regexp.MustCompile(verbLine), "pass": regexp.MustCompile(passLine), "alert": regexp.MustCompile(alertLine),
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
	tags := map[string]bool{}
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
			if len(pickle.Tags) != 1 || tags[pickle.Tags[0].Name] {
				t.Errorf("%s: %s needs one tag of its own", file, pickle.Name)
				continue
			}
			tags[pickle.Tags[0].Name] = true
			always := pickle.Tags[0].Name == "@always"
			if !always && (len(pickle.Steps) == 0 || !shapes["setup"].MatchString(pickle.Steps[0].Text) ||
				pickle.Steps[0].Type != messages.PickleStepType_CONTEXT) {
				t.Errorf("%s: %s does not start with Given empty nodes, empty VMs or a running cluster", file, pickle.Name)
			}
			for _, step := range pickle.Steps {
				var matched []string
				for name, re := range shapes {
					m := re.FindStringSubmatch(step.Text)
					if m == nil {
						continue
					}
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
				if len(matched) != 1 {
					t.Errorf("%s: %q matches %v, want one line shape", file, step.Text, matched)
				}
			}
		}
	}
}
