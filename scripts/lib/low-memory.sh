# shellcheck shell=bash
# A box with under 1 GB of memory: say so, and on Linux offer swap.
#
# Sourced by install.sh; defines functions and does nothing on its own.
#
#   lm_mem_kb                  prints the total memory in kB (0 if unknown)
#   lm_swap_kb                 prints the swap already configured, in kB
#   lm_low_memory_check        warns under 1 GB; on Linux offers a swap file
#
# Why: a 512 MB VPS had the kernel's out-of-memory killer take the VPN daemon
# (and fwupd, an update helper that serves no purpose on a server), and the
# box stopped answering DNS. Swap does not make a small box fast; it gives the
# kernel somewhere to put idle pages instead of killing a process.
#
# LM_MEMINFO overrides /proc/meminfo, LM_SWAPFILE the swap file's path, and
# LM_SWAP_MB its size (tests use all three). SOVEREIGN_DNS_SWAP=1 or 0 answers
# the question without asking. With nobody at the terminal and no answer set,
# it warns and changes nothing.

LM_LOW_KB=1048576   # 1 GiB; a "1 GB" VPS reports less than this

lm_mem_kb() {
  awk '/^MemTotal:/ { print $2; found = 1; exit } END { if (!found) print 0 }' "${LM_MEMINFO:-/proc/meminfo}" 2>/dev/null || echo 0
}

lm_swap_kb() {
  awk '/^SwapTotal:/ { print $2; found = 1; exit } END { if (!found) print 0 }' "${LM_MEMINFO:-/proc/meminfo}" 2>/dev/null || echo 0
}

# Adds a swap file and turns it on now and at every boot. Needs root.
lm_add_swap() {
  local file=${LM_SWAPFILE:-/swapfile} mb=${LM_SWAP_MB:-1024} sudo=""
  if [[ $(id -u) -ne 0 ]]; then
    command -v sudo >/dev/null 2>&1 || { echo "Adding swap needs sudo, which isn't here."; return 1; }
    sudo=sudo
    echo "Adding swap needs your computer password (sudo)."
  fi
  if [[ -e "$file" ]]; then
    echo "$file already exists, so it was not touched."
    return 1
  fi
  # dd, not fallocate: some filesystems (and older kernels) refuse swap on a
  # file made with fallocate.
  $sudo dd if=/dev/zero of="$file" bs=1M count="$mb" status=none &&
  $sudo chmod 600 "$file" &&
  $sudo mkswap "$file" >/dev/null &&
  $sudo swapon "$file" || {
    $sudo rm -f "$file"
    echo "Couldn't turn on swap here (some VPS and container setups don't allow it). Carrying on without it."
    return 1
  }
  # Only the live swap is written down once it works, so a failed attempt
  # never leaves a boot entry that points at nothing.
  if [[ -w /etc/fstab || -n "$sudo" ]] && ! grep -qs "^$file[[:space:]]" /etc/fstab; then
    printf '%s none swap sw 0 0\n' "$file" | $sudo tee -a /etc/fstab >/dev/null ||
      echo "Swap is on, but couldn't be saved to /etc/fstab, so it will be gone after a reboot."
  fi
  echo "Swap is on: $((mb / 1024)) GB at $file."
}

lm_low_memory_check() {
  local kb swap answer
  kb=$(lm_mem_kb)
  [[ "$kb" =~ ^[0-9]+$ && "$kb" -gt 0 && "$kb" -lt "$LM_LOW_KB" ]] || return 0
  swap=$(lm_swap_kb)
  echo
  echo "This computer has only about $((kb / 1024)) MB of memory. The box needs roughly 1 GB:"
  echo "with less, Linux can kill its VPN program when memory runs out, and DNS stops."
  echo "(The box now restarts that program by itself, but a small computer still struggles.)"
  if [[ "$(uname -s)" != Linux ]]; then
    echo "Give Docker Desktop more memory in its settings if you can."
    return 0
  fi
  echo "If this is a small VPS, also turn off its firmware updater, which it never needs:"
  echo "    sudo systemctl disable --now fwupd fwupd-refresh.timer"
  if [[ "$swap" =~ ^[0-9]+$ && "$swap" -gt 0 ]]; then
    echo "It already has swap ($((swap / 1024)) MB), so nothing to add."
    return 0
  fi
  answer=${SOVEREIGN_DNS_SWAP:-}
  if [[ -z "$answer" ]]; then
    if [[ -t 0 ]]; then
      printf 'Add a 1 GB swap file so it can cope? [Y/n] '
      read -r answer || answer=n
      case "$answer" in [Nn]*) answer=0 ;; *) answer=1 ;; esac
    else
      echo "No swap is set up. Run again at a terminal to be offered it, or set SOVEREIGN_DNS_SWAP=1."
      return 0
    fi
  fi
  case "$answer" in
    1) lm_add_swap || true ;;
    *) echo "Skipping swap." ;;
  esac
}
