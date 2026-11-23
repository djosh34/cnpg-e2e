Feature: Upgrade
  install.yml takes a running cluster from A to B, as production takes a
  new versions file. A is main's versions file with k3s.previous of B, the
  minor before; B is the run's. k3s restarts on one node after another,
  helmfile upgrades the releases whose manifests changed, and their pods
  roll to the new images.

  @upgrade
  Scenario: install.yml upgrades k3s and the images while the client writes and reads
    Given empty nodes
    When main's versions file is installed
    Then the cluster is healthy
    When the versions file is installed
    Then every node is on the versions file's k3s
    And every running container is on an image of the versions file
    And the cluster is healthy

  @upgrade-kvm
  Scenario: install.yml upgrades k3s and the images on VMs with SELinux enforcing
    Given empty VMs
    When main's versions file is installed
    Then the cluster is healthy
    When the versions file is installed
    Then every node is on the versions file's k3s
    And every running container is on an image of the versions file
    And the cluster is healthy
    And SELinux is enforcing
