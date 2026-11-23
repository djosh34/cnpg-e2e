Feature: Install
  install.yml installs k3s and every chart on new machines, as production
  does, and a second run changes nothing. The KVM machines are Rocky Linux 9
  VMs with SELinux enforcing.

  @install
  Scenario: install.yml installs everything on empty nodes
    Given empty nodes
    When the versions file is installed
    Then install.yml is idempotent

  @install-kvm
  Scenario: install.yml installs everything on empty VMs with SELinux enforcing
    Given empty VMs
    When the versions file is installed
    Then install.yml is idempotent
    And SELinux is enforcing
