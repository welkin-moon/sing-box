#!/usr/bin/env python3
"""Idempotently enable IPv6 HTTPS on JPX with local nginx safety rollback.

Run on the VPS as root after verifying a working IPv6 default route and
external IPv6 connectivity. Does not touch the TCP proxy ports or secrets.
"""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import subprocess


def patched(source: str) -> str:
    if 'server_name jpx.20100823.xyz;' not in source:
        raise ValueError('Refusing: this is not the JPX vhost')
    if 'ssl_certificate_key' not in source:
        raise ValueError('Refusing: origin TLS certificate is not configured')
    if 'listen [::]:443 ssl;' in source:
        return source
    needle = '    listen 443 ssl;'
    if source.count(needle) != 1:
        raise ValueError('Expected one IPv4 HTTPS listener in the JPX vhost')
    return source.replace(needle, needle + '\n    listen [::]:443 ssl;', 1)


def run(path: Path, test_only: bool) -> bool:
    original = path.read_text(encoding='utf-8')
    candidate = patched(original)
    if candidate == original:
        return False
    if test_only:
        return True
    backup = path.with_name(path.name + '.before-ipv6')
    # Preserve rollback material once: never overwrite the known-good snapshot.
    if not backup.exists():
        backup.write_bytes(original.encode('utf-8'))
        backup.chmod(0o600)
    candidate_path = path.with_name(path.name + '.ipv6-pending')
    candidate_path.write_text(candidate, encoding='utf-8')
    candidate_path.chmod(path.stat().st_mode & 0o777)
    candidate_path.replace(path)
    try:
        subprocess.run(['nginx', '-t'], check=True)
        subprocess.run(['systemctl', 'reload', 'nginx'], check=True)
    except Exception:
        path.write_text(original, encoding='utf-8')
        subprocess.run(['nginx', '-t'], check=False)
        subprocess.run(['systemctl', 'reload', 'nginx'], check=False)
        raise
    return True


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument('--path', type=Path, default=Path('/etc/nginx/sites-available/jpx-sub.conf'))
    ap.add_argument('--check-only', action='store_true')
    a = ap.parse_args()
    changed = run(a.path, a.check_only)
    print('needs-ipv6-listener' if a.check_only and changed else
          'already-configured' if not changed else 'ipv6-listener-reloaded')


if __name__ == '__main__':
    main()
