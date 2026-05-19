job "@@JOB_ID@@" {
  type        = "batch"
  namespace   = "@@NAMESPACE@@"
  datacenters = ["dc1"]

  # Ephemeral: no restart, no reschedule. A failed runner job means
  # the GHA job failed; rescheduling would just queue another runner
  # against the same job ID, which GitHub doesn't allow.
  reschedule {
    attempts = 0
    unlimited = false
  }

  group "runner" {
    count = 1

    restart {
      attempts = 0
      mode     = "fail"
    }

    task "runner" {
      driver = "podman"

      config {
        image      = "@@RUNNER_IMAGE@@"
        privileged = false
        # The runner needs to docker/podman build inside itself for
        # most workflows. Easiest path is mount the host's podman
        # socket; alternatives (DinD) come later if isolation needs
        # tightening.
        volumes = [
          "/run/podman/podman.sock:/var/run/docker.sock:rw"
        ]
      }

      env {
        RUNNER_URL       = "@@RUNNER_URL@@"
        RUNNER_TOKEN     = "@@RUNNER_TOKEN@@"
        RUNNER_LABELS    = "@@RUNNER_LABELS@@"
        RUNNER_EPHEMERAL = "true"
        # The runner image is expected to honour these env vars in
        # its entrypoint and shell out to `config.sh` + `run.sh`.
      }

      resources {
        cpu    = @@CPU@@
        memory = @@MEMORY@@
      }

      logs {
        max_files     = 2
        max_file_size = 10
      }
    }
  }
}
