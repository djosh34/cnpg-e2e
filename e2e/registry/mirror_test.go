package registry

import "testing"

func TestNewestTag(t *testing.T) {
	tags := []string{
		"18", "18.7", "18.7-debian13", "18.10-debian13", "18.8-debian13", "19.1-debian13",
		"18.9-alpine3.24", "18.11-debian13-fips", "18.12-dev", "sha256-0f00.sig", "latest",
		"1.21.3", "1.22.0", "1.21", "2.0.0", "1.22.0-rc.1",
		"0.165.0-contrib", "0.170.1-contrib", "0.171.0", "1.0.0-contrib",
		"v1.2.3", "v1.10.0", "1.11.0",
	}
	for current, want := range map[string]string{
		"18.7-debian13":   "18.10-debian13",
		"1.21.3":          "1.22.0",
		"0.165.0-contrib": "0.170.1-contrib",
		"v1.2.3":          "v1.10.0",
		"3.1.2":           "3.1.2",
	} {
		got, err := newestTag(tags, current)
		if err != nil || got != want {
			t.Errorf("newestTag(%s) = %s, %v; want %s", current, got, err, want)
		}
	}
	if _, err := newestTag(tags, "latest"); err == nil {
		t.Error("newestTag(latest): no error")
	}
}
