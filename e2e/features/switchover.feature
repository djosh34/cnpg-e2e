Feature: Switchover
  A promotion is kubectl cnpg promote.

  @switchover
  Scenario: A switchover to a replica
    Given a running cluster
    When replica 1 is promoted
    Then replica 1 is primary < 300 s

  @quiet-rewind
  Scenario: A switchover with no writes
    A former primary that rewinds onto an idle primary can wait for that
    primary's next WAL record. One write ends the wait.

    Given a running cluster
    And no writes
    When replica 1 is promoted
    Then replica 1 is primary < 300 s
    When 600 s pass
    And a row is written
    Then the cluster is healthy < 10 s
