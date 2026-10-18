#!/usr/bin/env python3
"""Offline candidate generator for a dedicated VLESS/TLS + Multiplex Brutal test port.

This is *not* a production publisher. It does not install the TCP Brutal kernel
module and does not verify socket/group-level v2 operation. It never replaces
the existing HY2 or REALITY+Vision listeners. Keep output private.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import uuid


def rate_mbps(value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not (1 <= value <= 100000):
        raise ValueError('Rate must be an integer, in measured Mbps')
    return value


def render(identity: dict, region: str, sni: str, hostnames: dict[str, str],
           cert_dir: Path, port: int, up_mbps: int, down_mbps: int) -> dict:
    if not re.fullmatch(r'[A-Za-z][A-Za-z0-9]{0,7}', region):
        raise ValueError('Invalid region label')
    if not re.fullmatch(r'[A-Za-z0-9.-]+', sni) or '.' not in sni:
        raise ValueError('Invalid TLS SNI')
    if not isinstance(port, int) or isinstance(port, bool) or not (1024 <= port <= 65535):
        raise ValueError('Invalid separate canary TCP port')
    up_mbps, down_mbps = rate_mbps(up_mbps), rate_mbps(down_mbps)
    chain, private = cert_dir / 'fullchain.pem', cert_dir / 'privkey.pem'
    if not chain.is_file() or not private.is_file():
        raise FileNotFoundError('On-host certificate and private key required')
    sources = identity.get('users')
    if not isinstance(sources, dict) or not sources:
        raise ValueError('User UUID mapping unavailable')
    users = []
    for name, user_uuid in sorted(sources.items()):
        if not re.fullmatch(r'ss[1-9][0-9]*', name):
            raise ValueError('Unexpected subscriber name')
        users.append({'name': name, 'uuid': str(uuid.UUID(user_uuid))})
    # Brutal settings are on Multiplex, *not* an XTLS-Vision flow.
    mux = {
        'enabled': True, 'padding': True,
        'brutal': {'enabled': True, 'up_mbps': up_mbps, 'down_mbps': down_mbps}
    }
    inbound = {
        'type': 'vless', 'tag': f'{region.lower()}-tcp-brutal-canary',
        'listen': '::', 'listen_port': port, 'users': users,
        'tls': {'enabled': True, 'server_name': sni,
                'certificate_path': str(chain), 'key_path': str(private)},
        'multiplex': mux,
    }
    server = {'log': {'level': 'warn'}, 'inbounds': [inbound],
              'outbounds': [{'type': 'direct', 'tag': 'direct'}],
              'route': {'final': 'direct', 'auto_detect_interface': True}}
    # Each subscriber receives only their own UUID in their own private bundle.
    # A base64 VLESS URI cannot represent the TCP Brutal multiplex settings
    # portably; users must import the JSON outbound into a compatible sing-box.
    client_outbounds_by_user = {}
    for user in users:
        individual = {}
        for family, host in hostnames.items():
            if family not in ('v4', 'v6', 'dual') or not isinstance(host, str):
                raise ValueError('Only v4/v6/dual family hostnames accepted')
            if not re.fullmatch(r'[A-Za-z0-9.-]+', host):
                raise ValueError('Invalid family hostname')
            individual[family] = {
                'type': 'vless', 'tag': f'CANARY-{region.upper()}-TCPBrutal-{family}',
                'server': host, 'server_port': port, 'uuid': user['uuid'],
                'tls': {'enabled': True, 'server_name': sni},
                'multiplex': {**mux, 'protocol': 'smux'},
            }
        client_outbounds_by_user[user['name']] = individual
    # Retain the original single-user preview key for older installer tooling.
    preview = client_outbounds_by_user[users[0]['name']]
    return {'server_candidate': server,
            'client_canary_outbounds': preview,
            'client_canary_outbounds_by_user': client_outbounds_by_user,
            'warning': 'Candidate only; kernel module and endpoint tests required before public subscription publication'}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument('--identity-file', type=Path, required=True,
                    help='private user identity json stored only on the server')
    ap.add_argument('--hostnames-file', type=Path, required=True,
                    help='private JSON map of family to DNS name, candidate only')
    ap.add_argument('--cert-dir', type=Path, required=True)
    ap.add_argument('--region', required=True)
    ap.add_argument('--tls-sni', required=True)
    ap.add_argument('--port', type=int, default=14443)
    ap.add_argument('--up-mbps', type=int, required=True)
    ap.add_argument('--down-mbps', type=int, required=True)
    ap.add_argument('--output', type=Path, required=True)
    a = ap.parse_args()
    data = render(json.loads(a.identity_file.read_text()),
                  a.region, a.tls_sni,
                  json.loads(a.hostnames_file.read_text()),
                  a.cert_dir, a.port, a.up_mbps, a.down_mbps)
    a.output.parent.mkdir(parents=True, exist_ok=True)
    a.output.write_text(json.dumps(data, indent=2) + '\n', encoding='utf-8')
    a.output.chmod(0o600)
    print('candidate_written_to_private_file; validate with sing-box check and external probes')


if __name__ == '__main__':
    main()
