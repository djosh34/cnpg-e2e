Feature: Image upgrade
  The scenario starts from an earlier versions file: the versions.yaml of the
  newest release that has one whose images differ from the checkout's, else
  the newest such version of versions/versions.yaml in the checkout's
  history, with the checkout's k3s. upgrade.yml deployed every chart with
  it. Then upgrade.yml takes
  the checkout's versions file, as production takes a new one: helmfile
  upgrades only the releases whose manifests changed, and their workloads
  roll to the new images. The nodes keep the images of before, so the image
  check takes both versions files.

  Background:
    Given the cluster is healthy

  @image-upgrade
  Scenario: upgrade.yml takes a new versions file while the client writes and reads
    When the versions file of the checkout is applied with upgrade.yml
    Then every running container is on an image of the versions file < 600 s
    And the releases whose manifests did not change are at their old revisions
    And the cluster is healthy < 600 s
