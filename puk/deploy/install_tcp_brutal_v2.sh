#!/usr/bin/env bash
# PUK: pinned TCP Brutal v2.0.1 DKMS installer for Debian 12/13.
# Read-only by default. --install changes kernel modules but not proxy or SSH routes.
set -Eeuo pipefail

VERSION=2.0.1
PINNED_COMMIT=d2397ff8bca04a29fd2de01cf7d2d4b825224de8
REPO=https://github.com/HyNetworks/tcp-brutal.git
MODULE=tcp-brutal

log() { printf '%s\n' "[puk-brutal] $*"; }
die() { log "ERROR: $*" >&2; exit 1; }
MODE=check
if (( $# > 0 )); then
  case "$1" in
    --check) MODE=check ;;
    --install) MODE=install ;;
    *) die 'Usage: install_tcp_brutal_v2.sh [--check|--install]' ;;
  esac
fi

[[ $(id -u) == 0 ]] || die 'Run as root'
[[ -r /etc/os-release ]] || die 'Missing /etc/os-release'
source /etc/os-release
[[ "$ID" == debian ]] || die 'Debian required'
[[ "$VERSION_ID" == 12 || "$VERSION_ID" == 13 ]] || die 'Debian 12 or 13 required'
[[ $(uname -s) == Linux ]] || die 'Linux required'
KERNEL=$(uname -r)
HEADERS="/lib/modules/$KERNEL/build"
CURRENT_CC=$(sysctl -n net.ipv4.tcp_congestion_control)
MODULE_INSTALLED=false
if command -v modinfo >/dev/null && modinfo brutal >/dev/null 2>&1; then MODULE_INSTALLED=true; fi
log "Debian=$VERSION_ID; kernel=$KERNEL; headers=$([[ -d $HEADERS ]] && echo yes || echo no)"
log "module=$MODULE_INSTALLED; default_cc=$CURRENT_CC; mode=$MODE"

if [[ "$MODE" == check ]]; then
  if command -v dkms >/dev/null; then dkms status -m "$MODULE" || true; fi
  if command -v brutalctl >/dev/null; then brutalctl list || true; fi
  [[ "$CURRENT_CC" != brutal ]] || die 'Unsafe global Brutal default detected'
  exit 0
fi

[[ "$CURRENT_CC" != brutal ]] || die 'Refusing installation while global CC=brutal'
[[ -d "$HEADERS" ]] || die "Missing headers for RUNNING kernel $KERNEL; prepare image+headers then reboot separately"
command -v flock >/dev/null || die 'Missing flock'
exec 9>/run/lock/puk-brutal-dkms.lock
flock -n 9 || die 'Another Brutal install holds the deployment lock'
AVAIL_MB=$(df -Pm / | awk 'NR==2{print $4}')
[[ "$AVAIL_MB" =~ ^[0-9]+$ ]] || die 'Cannot check free disk'
(( AVAIL_MB >= 850 )) || die "Only $AVAIL_MB MB free; need at least 850MB"

export DEBIAN_FRONTEND=noninteractive
LOGFILE="/var/log/puk-tcp-brutal-$VERSION.log"
log 'Preparing pinned source; no reboot or global congestion-control change'
apt-get update -qq >>"$LOGFILE" 2>&1
apt-get install -y -qq ca-certificates dkms gcc make git libc6-dev linux-libc-dev >>"$LOGFILE" 2>&1
BUILD=$(mktemp -d /tmp/puk-brutal-src.XXXXXXXX)
trap 'rm -rf "$BUILD"' EXIT
git clone -q --depth 1 --branch v2.0.1 "$REPO" "$BUILD/src" >>"$LOGFILE" 2>&1
ACTUAL=$(git -C "$BUILD/src" rev-parse HEAD)
[[ "$ACTUAL" == "$PINNED_COMMIT" ]] || die "Unexpected upstream commit $ACTUAL"
log "Source pinned: $PINNED_COMMIT"

SOURCE="/usr/src/$MODULE-$VERSION"
if [[ -e "$SOURCE" ]]; then
  grep -Eq '^#define BRUTAL_VERSION_MAJOR[[:space:]]+2([[:space:]]|$)' "$SOURCE/brutal.h" ||
    die 'Unrecognized existing module source'
else
  install -d -m 0755 "$SOURCE/tools" "$SOURCE/scripts"
  cp "$BUILD/src"/brutal.h "$BUILD/src"/brutal_*.c "$BUILD/src"/Makefile "$BUILD/src"/.clang-format "$SOURCE/"
  cp "$BUILD/src"/tools/brutalctl.c "$BUILD/src"/tools/Makefile "$SOURCE/tools/"
  cp "$BUILD/src"/scripts/mkdkmsconf.sh "$SOURCE/scripts/"
  (cd "$SOURCE"; bash scripts/mkdkmsconf.sh > dkms.conf)
fi
if ! dkms status -m "$MODULE" -v "$VERSION" 2>/dev/null | grep -q "$MODULE"; then
  dkms add -m "$MODULE" -v "$VERSION" >>"$LOGFILE" 2>&1
fi
if ! dkms status -m "$MODULE" -v "$VERSION" -k "$KERNEL" 2>/dev/null | grep -q installed; then
  dkms build -m "$MODULE" -v "$VERSION" -k "$KERNEL" >>"$LOGFILE" 2>&1
  dkms install -m "$MODULE" -v "$VERSION" -k "$KERNEL" >>"$LOGFILE" 2>&1
fi
modprobe brutal || die 'Could not load brutal kernel module'
make -C "$SOURCE/tools" -s >>"$LOGFILE" 2>&1
install -m 0755 "$SOURCE/tools/brutalctl" /usr/local/bin/brutalctl
# Systemd loads the module after reboot; no global CC or broad route rules.
MODULES_FILE=/etc/modules-load.d/95-puk-brutal.conf
if [[ -e "$MODULES_FILE" ]]; then
  grep -qx brutal "$MODULES_FILE" ||
    die 'Unexpected existing modules-load entry; inspect before replacing'
else
  install -d -m 0755 /etc/modules-load.d
  printf 'brutal\n' > "$MODULES_FILE"
  chmod 0644 "$MODULES_FILE"
fi
grep -qw brutal /proc/sys/net/ipv4/tcp_available_congestion_control ||
  die 'brutal is not an available TCP congestion controller after modprobe'
[[ $(sysctl -n net.ipv4.tcp_congestion_control) == "$CURRENT_CC" ]] ||
  die 'System default CC changed unexpectedly'
log 'TCP Brutal v2.0.1 module installed, active in available algorithms'
log 'No rules, SSH routes, inbound listeners, or subscriptions changed'
