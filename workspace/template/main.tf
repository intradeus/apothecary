# Coder template "factory" (DESIGN.md §11, §12): one dev container per
# workspace on its own network. Parameters carry ONLY non-secrets (run_id,
# repo_url, branch) — secrets arrive via the engine's bootstrap WriteFiles.
# The host.docker.internal -> host-gateway mapping is what lets the shim
# reach the engine and the agent reach Coder in localhost mode (§8).
#
# Project services (db/queue/redis) are user-authored: add docker_container
# resources on docker_network.workspace as the project needs them.

terraform {
  required_providers {
    coder = {
      source = "coder/coder"
    }
    docker = {
      source = "kreuzwerker/docker"
    }
  }
}

provider "coder" {}
provider "docker" {}

data "coder_workspace" "me" {}
data "coder_workspace_owner" "me" {}

data "coder_parameter" "run_id" {
  name         = "run_id"
  display_name = "Factory run id"
  type         = "string"
  mutable      = false
}

data "coder_parameter" "repo_url" {
  name         = "repo_url"
  display_name = "Repository URL"
  type         = "string"
  mutable      = false
}

data "coder_parameter" "branch" {
  name         = "branch"
  display_name = "Working branch"
  type         = "string"
  mutable      = false
}

variable "workspace_image" {
  description = "Pre-built factory workspace image"
  type        = string
  default     = "factory-workspace:latest"
}

variable "coder_port" {
  type    = number
  default = 7080
}

resource "coder_agent" "main" {
  os   = "linux"
  arch = "amd64"

  # Blocking: ssh (and therefore the engine's readiness probe + bootstrap)
  # waits until the firewall is up — agents can never run with open egress.
  startup_script_behavior = "blocking"
  startup_script          = <<-EOT
    set -e
    sudo /usr/local/bin/init-firewall.sh
    code-server --auth none --port 13337 >/tmp/code-server.log 2>&1 &
  EOT

  metadata {
    display_name = "CPU"
    key          = "cpu"
    script       = "coder stat cpu"
    interval     = 10
    timeout      = 1
  }
}

resource "coder_app" "code-server" {
  agent_id     = coder_agent.main.id
  slug         = "code-server"
  display_name = "VS Code"
  url          = "http://localhost:13337/?folder=/home/factory/repo"
  icon         = "/icon/code.svg"
  subdomain    = false
  share        = "owner"
}

resource "docker_network" "workspace" {
  name = "factory-${lower(data.coder_parameter.run_id.value)}"
}

resource "docker_volume" "home" {
  name = "factory-home-${lower(data.coder_parameter.run_id.value)}"
  # Keep home across container rebuilds within the run; destroyed with the
  # workspace.
}

resource "docker_container" "workspace" {
  count = data.coder_workspace.me.start_count
  name  = "factory-ws-${lower(data.coder_parameter.run_id.value)}"
  image = var.workspace_image

  hostname = "factory-${lower(data.coder_parameter.run_id.value)}"

  entrypoint = ["sh", "-c", replace(coder_agent.main.init_script, "/localhost|127\\.0\\.0\\.1/", "host.docker.internal")]

  env = [
    "CODER_AGENT_TOKEN=${coder_agent.main.token}",
    "FACTORY_REPO_URL=${data.coder_parameter.repo_url.value}",
    "FACTORY_BRANCH=${data.coder_parameter.branch.value}",
    "FACTORY_CODER_PORT=${var.coder_port}",
  ]

  # §8/§11: containers cannot reach the host's 127.0.0.1 — this mapping is
  # what makes localhost mode work.
  host {
    host = "host.docker.internal"
    ip   = "host-gateway"
  }

  networks_advanced {
    name = docker_network.workspace.name
  }

  # NET_ADMIN solely for init-firewall.sh; no docker socket is mounted.
  capabilities {
    add = ["NET_ADMIN"]
  }

  volumes {
    container_path = "/home/factory"
    volume_name    = docker_volume.home.name
  }
}
