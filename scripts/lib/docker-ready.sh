# shellcheck shell=bash
# Get Docker ready: installed, running, answering, and set to start by itself
# after a reboot.
#
# Sourced by install.sh; defines functions and does nothing on its own.
#
#   docker_ready     the whole job; returns 0 once `docker info` answers
#
# The steps, usable on their own:
#   dr_os               macos | linux | wsl | windows | unknown
#   dr_find_docker      0 if a docker CLI is on PATH (adds Docker.app's to PATH)
#   dr_install_docker   installs a pinned Docker (asks for the admin password)
#   dr_start_docker     starts the engine
#   dr_wait_docker      waits until `docker info` answers
#   dr_enable_autostart turns on start-at-login (macOS) / start-at-boot (Linux)
#
# Everything is pinned and checked before it runs:
#   macOS  Docker Desktop DMG, one exact build, sha256 checked
#          (the same build and hashes as Homebrew's docker-desktop cask).
#   Linux  Docker's own get.docker.com script at one exact commit, sha256
#          checked, told to install one exact engine version. Its packages
#          come from Docker's apt/dnf repo and are checked by Docker's key.
#
# Knobs (tests use these):
#   DR_DRY_RUN=1          print installs and privileged commands, run none
#   DR_WAIT_SECS=300      how long to wait for the engine to answer
#   DR_LAUNCH_AGENTS_DIR  where the macOS login agent goes (~/Library/LaunchAgents)
#   DR_DOCKER_SETTINGS    Docker Desktop's settings-store.json

DR_MAC_DESKTOP_VERSION="4.94.0"
DR_MAC_DESKTOP_BUILD="241994"
DR_MAC_DESKTOP_SHA256_ARM64="02147e4d559ff41e1d9d7be63a554101340237064c7b6df234548aa111ebf04c"
DR_MAC_DESKTOP_SHA256_AMD64="44a9c8524c6f5cc6ba51cec7fd6343b2a744ff4cb5126f7d3d5b6f4da06fd905"
DR_MAC_MIN_MAJOR=14 # Docker Desktop 4.94 needs macOS 14 Sonoma or newer.

DR_GET_DOCKER_COMMIT="2b32480025b223ebfddae9a3a8bef09027680f53"
DR_GET_DOCKER_SHA256="fefa50ccd50efb42f438b506fc3a88574118f314aaf2a7cd5b6e1ffb1bffcf26"
DR_LINUX_ENGINE_VERSION="${DR_LINUX_ENGINE_VERSION:-29.4}"

DR_AGENT_LABEL="com.sovereign-dns.docker-autostart"

dr_log() { printf '%s\n' "$*" >&2; }

# Run a command that changes the machine, or print it under DR_DRY_RUN.
dr_run() {
  if [[ "${DR_DRY_RUN:-0}" == "1" ]]; then
    dr_log "[dry-run] $*"
    return 0
  fi
  "$@"
}

# "sudo" unless we are root. Empty output means "run directly".
dr_sudo() {
  if [[ "$(id -u)" == "0" ]]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$@"
  else
    dr_log "This step needs administrator rights, and 'sudo' isn't available. Run the installer as root."
    return 1
  fi
}

dr_os() {
  case "$(uname -s)" in
    Darwin) echo macos ;;
    Linux)
      if grep -qi microsoft /proc/version 2>/dev/null; then echo wsl; else echo linux; fi ;;
    MINGW* | MSYS* | CYGWIN*) echo windows ;;
    *) echo unknown ;;
  esac
}

dr_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# dr_verify_sha256 FILE EXPECTED: 0 only on an exact match.
dr_verify_sha256() {
  local got
  got=$(dr_sha256 "$1") || return 1
  if [[ "$got" != "$2" ]]; then
    dr_log "Checksum mismatch for $(basename "$1"):"
    dr_log "  expected $2"
    dr_log "  got      $got"
    dr_log "The download is not the file this installer was built for. Nothing was installed."
    return 1
  fi
}

dr_download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --progress-bar -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    dr_log "Need curl or wget to download $1."
    return 1
  fi
}

dr_windows_help() {
  dr_log "Windows: this installer runs inside WSL (Ubuntu), with Docker Desktop for Windows."
  dr_log "  1. In PowerShell:  winget install -e --id Docker.DockerDesktop"
  dr_log "     (or download it from https://docs.docker.com/desktop/setup/install/windows-install/)"
  dr_log "  2. Open Docker Desktop. In Settings > General turn on 'Start Docker Desktop when you sign in',"
  dr_log "     and in Settings > Resources > WSL integration turn on your Ubuntu distro."
  dr_log "  3. Open Ubuntu (WSL) and paste the install line again."
}

dr_find_docker() {
  command -v docker >/dev/null 2>&1 && return 0
  # Docker Desktop installs its CLI inside the app and links it into
  # /usr/local/bin only on first launch, so a fresh install isn't on PATH yet.
  local dir
  for dir in /Applications/Docker.app/Contents/Resources/bin "$HOME/.orbstack/bin"; do
    if [[ -x "$dir/docker" ]]; then
      PATH="$dir:$PATH"
      export PATH
      return 0
    fi
  done
  return 1
}

# Which engine backs the docker CLI on macOS: desktop | orbstack | colima.
dr_mac_engine() {
  local host
  host=$(docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null)
  case "$host" in
    *orbstack*) echo orbstack ;;
    *colima*) echo colima ;;
    *)
      if [[ -d /Applications/Docker.app ]]; then echo desktop
      elif [[ -d /Applications/OrbStack.app ]]; then echo orbstack
      elif command -v colima >/dev/null 2>&1; then echo colima
      else echo desktop
      fi ;;
  esac
}

dr_install_docker_mac() {
  local major arch sha url tmp
  major=$(sw_vers -productVersion | cut -d. -f1)
  if ((major < DR_MAC_MIN_MAJOR)); then
    dr_log "Docker Desktop needs macOS $DR_MAC_MIN_MAJOR or newer; this Mac runs $(sw_vers -productVersion). Update macOS first."
    return 1
  fi
  case "$(uname -m)" in
    arm64) arch=arm64; sha=$DR_MAC_DESKTOP_SHA256_ARM64 ;;
    x86_64) arch=amd64; sha=$DR_MAC_DESKTOP_SHA256_AMD64 ;;
    *) dr_log "Unsupported Mac CPU: $(uname -m)"; return 1 ;;
  esac
  url="https://desktop.docker.com/mac/main/$arch/$DR_MAC_DESKTOP_BUILD/Docker.dmg"

  dr_log "Docker isn't installed. Installing Docker Desktop $DR_MAC_DESKTOP_VERSION ($arch)."
  if [[ "${DR_DRY_RUN:-0}" == "1" ]]; then
    dr_log "[dry-run] download $url"
    dr_log "[dry-run] check sha256 = $sha"
    dr_log "[dry-run] sudo <dmg>/Docker.app/Contents/MacOS/install --accept-license --user=$(id -un)"
    return 0
  fi

  tmp=$(mktemp -d) || return 1
  if ! dr_download "$url" "$tmp/Docker.dmg" || ! dr_verify_sha256 "$tmp/Docker.dmg" "$sha"; then
    rm -rf "$tmp"
    return 1
  fi
  if ! hdiutil attach -nobrowse -readonly -quiet -mountpoint "$tmp/mnt" "$tmp/Docker.dmg"; then
    rm -rf "$tmp"
    return 1
  fi
  dr_log "Copying Docker into /Applications needs your Mac login password (typing is hidden)."
  local rc=0
  sudo "$tmp/mnt/Docker.app/Contents/MacOS/install" --accept-license --user="$(id -un)" || rc=$?
  hdiutil detach -quiet "$tmp/mnt" || true
  rm -rf "$tmp"
  return "$rc"
}

dr_install_docker_linux() {
  local tmp script url
  url="https://raw.githubusercontent.com/docker/docker-install/$DR_GET_DOCKER_COMMIT/install.sh"
  dr_log "Docker isn't installed. Installing Docker Engine $DR_LINUX_ENGINE_VERSION from Docker's own packages."
  if [[ "${DR_DRY_RUN:-0}" == "1" ]]; then
    dr_log "[dry-run] download $url"
    dr_log "[dry-run] check sha256 = $DR_GET_DOCKER_SHA256"
    dr_log "[dry-run] sudo sh install.sh --version $DR_LINUX_ENGINE_VERSION"
    return 0
  fi
  tmp=$(mktemp -d) || return 1
  script="$tmp/get-docker.sh"
  if ! dr_download "$url" "$script" || ! dr_verify_sha256 "$script" "$DR_GET_DOCKER_SHA256"; then
    rm -rf "$tmp"
    return 1
  fi
  local rc=0
  dr_sudo sh "$script" --version "$DR_LINUX_ENGINE_VERSION" || rc=$?
  rm -rf "$tmp"
  [[ $rc == 0 ]] || return "$rc"
  # Let this user run docker without sudo from the next login on. For the
  # rest of this run, dr_wait_docker falls back to sudo.
  if [[ "$(id -u)" != "0" ]]; then
    dr_sudo usermod -aG docker "$(id -un)" || true
  fi
}

dr_install_docker() {
  case "$(dr_os)" in
    macos) dr_install_docker_mac ;;
    linux) dr_install_docker_linux ;;
    wsl)
      dr_log "Docker isn't available in this WSL distro."
      dr_windows_help
      return 1 ;;
    windows) dr_windows_help; return 1 ;;
    *) dr_log "Unsupported system: $(uname -s). Install Docker yourself: https://docs.docker.com/get-docker/"; return 1 ;;
  esac
}

dr_has_systemd() { [[ -d /run/systemd/system ]] && command -v systemctl >/dev/null 2>&1; }

dr_start_docker() {
  case "$(dr_os)" in
    macos)
      case "$(dr_mac_engine)" in
        orbstack) dr_run open -g -a OrbStack ;;
        colima) dr_run colima start ;;
        *) dr_run open -g -a Docker ;;
      esac ;;
    linux)
      if dr_has_systemd; then
        dr_run dr_sudo systemctl start docker
      elif command -v rc-service >/dev/null 2>&1; then
        dr_run dr_sudo rc-service docker start
      elif command -v service >/dev/null 2>&1 && [[ -x /etc/init.d/docker ]] &&
        dr_run dr_sudo service docker start; then
        :
      else
        # No init system (e.g. inside a container), or its sysvinit script
        # failed there (it can't set ulimits): run the daemon directly.
        dr_run dr_sudo sh -c 'nohup dockerd >/var/log/dockerd.log 2>&1 &'
      fi ;;
    wsl | windows)
      dr_log "Start Docker Desktop on Windows, wait until it says it's running, then run this again."
      dr_windows_help
      return 1 ;;
    *) return 1 ;;
  esac
}

# 0 when the engine answers. Falls back to sudo when only root can reach the
# socket (a user just added to the docker group gets it at next login), and
# from then on `docker` in this shell means `sudo docker`.
dr_docker_answers() {
  docker info >/dev/null 2>&1 && return 0
  if [[ "$(id -u)" != "0" ]] && command -v sudo >/dev/null 2>&1 &&
    docker info 2>&1 | grep -qi 'permission denied' && sudo docker info >/dev/null 2>&1; then
    dr_log "Using sudo for docker in this run (your docker group membership starts at next login)."
    docker() { sudo "$(type -P docker)" "$@"; }
    return 0
  fi
  return 1
}

dr_wait_docker() {
  local limit=${DR_WAIT_SECS:-300} waited=0
  [[ "${DR_DRY_RUN:-0}" == "1" ]] && { dr_log "[dry-run] wait until 'docker info' answers"; return 0; }
  dr_log "Waiting for Docker to start (first start can take a minute or two)..."
  while ! dr_docker_answers; do
    if ((waited >= limit)); then
      dr_log "Docker didn't answer within ${limit}s."
      [[ "$(dr_os)" == macos ]] && dr_log "Open Docker from Applications, accept any prompts it shows, then run this again."
      return 1
    fi
    sleep 2
    waited=$((waited + 2))
    ((waited % 20 == 0)) && dr_log "  still waiting (${waited}s)"
  done
  dr_log "Docker is running."
}

# A per-user login agent that opens the engine at login. Written, not loaded:
# launchd loads everything in ~/Library/LaunchAgents at the next login, which
# is the only time it matters, and loading it now would poke a running Docker.
dr_write_launch_agent() {
  local dir=${DR_LAUNCH_AGENTS_DIR:-$HOME/Library/LaunchAgents} engine=$1 plist args
  plist="$dir/$DR_AGENT_LABEL.plist"
  case "$engine" in
    orbstack) args="    <string>/usr/bin/open</string><string>-g</string><string>-a</string><string>OrbStack</string>" ;;
    colima) args="    <string>$(command -v colima)</string><string>start</string>" ;;
    *) args="    <string>/usr/bin/open</string><string>-g</string><string>-a</string><string>Docker</string>" ;;
  esac
  if [[ "${DR_DRY_RUN:-0}" == "1" ]]; then
    dr_log "[dry-run] write $plist"
    return 0
  fi
  mkdir -p "$dir" || return 1
  cat > "$plist" << PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$DR_AGENT_LABEL</string>
  <key>ProgramArguments</key>
  <array>
$args
  </array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>
  <key>RunAtLoad</key><true/>
</dict>
</plist>
PLIST
  plutil -lint -s "$plist" || return 1
  dr_log "Login agent written: $plist"
}

# Docker Desktop's own "Start Docker Desktop when you sign in" switch.
dr_desktop_autostart_setting() {
  local settings=${DR_DOCKER_SETTINGS:-$HOME/Library/Group Containers/group.com.docker/settings-store.json}
  # Only exists after Docker Desktop's first launch; the login agent covers that case.
  [[ -f "$settings" ]] || return 0
  dr_run plutil -replace AutoStart -bool true "$settings" || return 1
  dr_log "Docker Desktop setting turned on: Start Docker Desktop when you sign in."
}

dr_enable_autostart() {
  case "$(dr_os)" in
    macos)
      local engine
      engine=$(dr_mac_engine)
      [[ "$engine" == desktop ]] && { dr_desktop_autostart_setting || return 1; }
      dr_write_launch_agent "$engine" || return 1
      if [[ "$(defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>/dev/null)" == "" ]]; then
        dr_log "Note: Docker on a Mac starts when someone logs in. For a box that should come back after a"
        dr_log "power cut with nobody there, turn on automatic login: System Settings > Users & Groups."
      fi ;;
    linux)
      if dr_has_systemd; then
        dr_run dr_sudo systemctl enable docker.service || return 1
        dr_run dr_sudo systemctl enable containerd.service 2>/dev/null || true
        [[ "${DR_DRY_RUN:-0}" == "1" ]] || [[ "$(systemctl is-enabled docker.service 2>/dev/null)" == enabled ]] || return 1
        dr_log "Docker is set to start at boot (systemd)."
      elif command -v rc-update >/dev/null 2>&1; then
        dr_run dr_sudo rc-update add docker default || return 1
        dr_log "Docker is set to start at boot (OpenRC)."
      else
        dr_log "No init system found (systemd or OpenRC), so Docker can't be set to start at boot here."
        return 1
      fi ;;
    wsl | windows)
      dr_log "On Windows, turn on Docker Desktop > Settings > General > 'Start Docker Desktop when you sign in'."
      return 1 ;;
    *) return 1 ;;
  esac
}

docker_ready() {
  local os
  os=$(dr_os)
  if [[ "$os" == windows ]]; then
    dr_windows_help
    return 1
  fi

  if ! dr_find_docker; then
    dr_install_docker || return 1
    if [[ "${DR_DRY_RUN:-0}" != "1" ]] && ! dr_find_docker; then
      dr_log "Docker was installed but its 'docker' command can't be found. Open a new terminal and run this again."
      return 1
    fi
  fi

  if [[ "${DR_DRY_RUN:-0}" == "1" ]] || ! dr_docker_answers; then
    dr_start_docker || return 1
    dr_wait_docker || return 1
  fi

  if [[ "${DR_DRY_RUN:-0}" != "1" ]] && ! docker compose version >/dev/null 2>&1; then
    dr_log "This Docker is missing the 'compose' plugin. Update Docker Desktop (or install docker-compose-plugin), then run this again."
    return 1
  fi

  # Not fatal: the box works now; only the after-reboot promise is at risk.
  if ! dr_enable_autostart; then
    dr_log "WARNING: Docker will NOT start by itself after a reboot on this machine. Start it by hand after restarts."
  fi
  return 0
}
