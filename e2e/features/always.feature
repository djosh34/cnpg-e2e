@always
Feature: What always holds
  The e2e runs this after every scenario once the cluster and the client
  are up, also after a failed line. Reading and writing are what the client
  got on rw over the whole scenario: a gap of at most 5 s in reading and
  180 s in writing, or the scenario's own limit of writing. Where the
  primary changed or stopped, reading takes the limit of writing. The alerts
  are healthy when none fires, and each alert that fired is one the scenario
  says fired, fires or may fire.

  Scenario: After every scenario
    Then the cluster is healthy
    And reading is healthy
    And writing is healthy
    And no acknowledged write is lost
    And the alerts are healthy
    And the dashboard is healthy
    And every running container is on an image of the versions file
