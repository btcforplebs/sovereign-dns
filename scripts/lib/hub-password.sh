# shellcheck shell=bash
# The dashboard password, asked the same way by scripts/hub-lan.sh and
# scripts/hub-mesh.sh. One password guards the hub from the home network and
# from the mesh (hub/auth.go, hub/mesh.go).
#
# Sourced; defines functions and does nothing on its own. Run from the
# sovereign-dns folder (it reads .env).
#
#   hub_ask_password   sets hub_pw. SOVEREIGN_DNS_HUB_PASSWORD answers without
#                      a terminal. Returns 1 when the user presses Ctrl-D
#                      ("never mind"); exits on a password it cannot store.
#   hub_env_password   prints the HUB_PASSWORD line already in .env, if any
#   hub_env_is KEY VAL true when .env has KEY=VAL

hub_ask_password() {
  hub_pw=${SOVEREIGN_DNS_HUB_PASSWORD:-}
  if [[ -z "$hub_pw" ]]; then
    if [[ ! -t 0 ]]; then
      printf '%s\n' "No terminal to ask for a password. Set SOVEREIGN_DNS_HUB_PASSWORD and run again." >&2
      exit 1
    fi
    local pw2
    while :; do
      echo "Pick a password for the dashboard, at least 8 characters (typing is hidden):"
      read -rs hub_pw || { echo; return 1; }
      echo
      echo "Type it again:"
      read -rs pw2 || { echo; return 1; }
      echo
      if [[ "$hub_pw" != "$pw2" ]]; then echo "Those didn't match. Try again."; continue; fi
      if [[ ${#hub_pw} -lt 8 ]]; then echo "That's shorter than 8 characters. Try again."; continue; fi
      if [[ "$hub_pw" == *"'"* ]]; then echo "Please pick one without a ' (single quote) in it."; continue; fi
      break
    done
  fi
  if [[ ${#hub_pw} -lt 8 ]]; then
    printf '%s\n' "The dashboard password must be at least 8 characters." >&2
    exit 1
  fi
  if [[ "$hub_pw" == *"'"* || "$hub_pw" == *$'\n'* ]]; then
    printf '%s\n' "The dashboard password can't contain a ' or a line break." >&2
    exit 1
  fi
}

hub_env_password() { grep -E "^HUB_PASSWORD='.+'$" .env | tail -n1 || true; }

hub_env_is() { grep -qx "$1=$2" .env; }
