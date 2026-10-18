#!/usr/bin/env python3
"""Render a JPX sing-box server and per-user subscription sources.

No actual endpoint credentials or origin authentication keys belong in git.
The renderer preserves existing on-host credential files and is idempotent.
"""
from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import uuid
from urllib.parse import quote, urlencode


def save(path: Path, data: str, mode: int = 0o600, group: str | None = None) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    pending = path.with_name(path.name + '.pending')
    with pending.open('w', encoding='utf-8') as out:
        out.write(data)
    pending.chmod(mode)
    if group and hasattr(os, 'geteuid') and os.geteuid() == 0:
        import grp
        os.chown(pending, -1, grp.getgrnam(group).gr_gid)
    pending.replace(path)


def load_or_make_keypair(state: Path, binary: str) -> tuple[str, str]:
    keypath = state / 'jpx-reality-keypair.txt'
    existing = Path('/etc/sing-box/jpx-keypair.txt')
    if not keypath.exists() and existing.is_file():
        save(keypath, existing.read_text(encoding='utf-8'), 0o600)
    if not keypath.exists():
        pair = subprocess.run([binary, 'generate', 'reality-keypair'], check=True,
                              text=True, capture_output=True)
        save(keypath, pair.stdout)
    data = keypath.read_text(encoding='utf-8')
    private = re.search(r'PrivateKey:\s*([A-Za-z0-9_-]{40,60})', data)
    public = re.search(r'PublicKey:\s*([A-Za-z0-9_-]{40,60})', data)
    if not private or not public:
        raise ValueError('Invalid Reality keypair format; refusing to overwrite')
    return private.group(1), public.group(1)


def render(state: Path, cert_dir: Path, config: Path, subs: Path,
           fqdn: str, reality_name: str, binary: str, users: int) -> dict:
    for p in (state, config, subs):
        if p.is_symlink():
            raise ValueError(f'Symlinked deployment path is not permitted: {p}')
    state.mkdir(parents=True, exist_ok=True)
    os.chmod(state, 0o750)
    if hasattr(os, 'geteuid') and os.geteuid() == 0:
        import grp
        os.chown(state, -1, grp.getgrnam('sing-box').gr_gid)
    cert = cert_dir / 'fullchain.pem'
    key = cert_dir / 'privkey.pem'
    if not cert.is_file() or not key.is_file():
        raise FileNotFoundError('Certificate chain/private key is not staged on this host')
    reality_private, reality_public = load_or_make_keypair(state, binary)
    ids_file = state / 'vless-identities.json'
    if ids_file.exists():
        ids = json.loads(ids_file.read_text(encoding='utf-8'))
    else:
        ids = {'short_id': secrets.token_hex(8), 'users': {}}
    ids.setdefault('users', {})
    hys = []
    vless = []
    all_links = {}
    for n in range(1, users + 1):
        name = f'ss{n}'
        hy_file = state / f'hy2-user{n}'
        if not hy_file.is_file():
            save(hy_file, secrets.token_urlsafe(32) + '\n')
        pw = hy_file.read_text(encoding='utf-8').strip()
        if len(pw) < 30:
            raise ValueError('HY2 credential length insufficient')
        ident = ids['users'].setdefault(name, str(uuid.uuid4()))
        uuid.UUID(ident)
        hys.append({'name': name, 'password': pw})
        vless.append({'name': name, 'uuid': ident, 'flow': 'xtls-rprx-vision'})
        hy_params = urlencode({'sni': fqdn, 'alpn': 'h3'})
        hy_uri = (f'hysteria2://{quote(pw, safe="")}@{fqdn}:11389?{hy_params}'
                  f'#JPX-Matrix-Direct-HY2-v4')
        reality_params = urlencode({'encryption': 'none', 'security': 'reality',
                                    'type': 'tcp', 'flow': 'xtls-rprx-vision',
                                    'sni': reality_name, 'fp': 'chrome',
                                    'pbk': reality_public, 'sid': ids['short_id']})
        reality_uri = (f'vless://{ident}@{fqdn}:11443?{reality_params}'
                       '#JPX-Matrix-Direct-Reality-v4')
        all_links[name] = [hy_uri, reality_uri]
    save(ids_file, json.dumps(ids, indent=2, sort_keys=True) + '\n')
    proxy = {
        'log': {'level': 'warn', 'timestamp': True},
        'inbounds': [
            {'type': 'hysteria2', 'tag': 'jpx-hy2-direct', 'listen': '::',
             'listen_port': 11389, 'users': hys,
             'tls': {'enabled': True, 'certificate_path': str(cert), 'key_path': str(key)}},
            {'type': 'vless', 'tag': 'jpx-reality-direct', 'listen': '::',
             'listen_port': 11443, 'users': vless,
             'tls': {'enabled': True, 'server_name': reality_name,
                     'reality': {'enabled': True,
                                 'handshake': {'server': reality_name, 'server_port': 443},
                                 'private_key': reality_private,
                                 'short_id': [ids['short_id']]}}}
        ],
        'outbounds': [{'type': 'direct', 'tag': 'direct'}],
        'route': {'final': 'direct', 'auto_detect_interface': True}
    }
    # Don't replace the running configuration until the caller has checked it.
    pending = config.with_name(config.name + '.next')
    save(pending, json.dumps(proxy, indent=2) + '\n', 0o640, group='sing-box')
    subs.mkdir(parents=True, exist_ok=True)
    os.chmod(subs, 0o750)
    if hasattr(os, 'geteuid') and os.geteuid() == 0:
        import grp
        os.chown(subs, -1, grp.getgrnam('www-data').gr_gid)
    for name, links in all_links.items():
        encoded = base64.b64encode(('\n'.join(links) + '\n').encode()).decode() + '\n'
        save(subs / name, encoded, 0o640, group='www-data')
    token_file = state / 'source-token'
    if not token_file.exists():
        save(token_file, secrets.token_urlsafe(36) + '\n')
    return {'users': users, 'inbounds': len(proxy['inbounds']),
            'subscription_files': len(all_links), 'config_candidate': str(pending),
            'token_file': str(token_file)}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument('--state-dir', type=Path, default=Path('/etc/jpx'))
    ap.add_argument('--cert-dir', type=Path, default=Path('/etc/sing-box/tls'))
    ap.add_argument('--config', type=Path, default=Path('/etc/jpx/proxy.json'))
    ap.add_argument('--subs-dir', type=Path, default=Path('/srv/jpx-subs'))
    ap.add_argument('--host', default='jpx.20100823.xyz')
    ap.add_argument('--reality-sni', default='www.sony.jp')
    ap.add_argument('--sing-box', default='sing-box')
    ap.add_argument('--users', type=int, default=8)
    a = ap.parse_args()
    if not 1 <= a.users <= 64:
        ap.error('users must be in 1..64')
    result = render(a.state_dir, a.cert_dir, a.config, a.subs_dir,
                    a.host, a.reality_sni, a.sing_box, a.users)
    print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    main()
