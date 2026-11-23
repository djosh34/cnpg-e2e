// Package e2e is the e2e test: TestScenario runs one scenario on the
// machine's own Docker. The lab (lab.go) is the services outside k3s, three
// nodes and the controller that runs Ansible. A scenario of the feature
// files in features/ starts the lab from the cache (cache.go), deploys every
// chart with upgrade.yml as production does, then runs its lines and those
// of always.feature against the cluster (feature.go).
package e2e
