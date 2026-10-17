#!/usr/bin/env python3
"""Plan TCP Brutal v2 PUK matrix entries, never silently publish guesses.

Input is a PRIVATE measurement/capability manifest written by the operator
after actual Linux module verification and external end-to-end checks.
This tool does NOT generate real credentials or claim a proxy works.
"""
from __future__ import annotations

import argparse
import ipaddress
import json
from pathlib import Path
import re

POLICIES = ("direct", "google", "all")
FAMILIES = ("v4", "v6", "dual")
REGION_RE = re.compile(r"^[A-Za-z][A-Za-z0-9]{0,7}$")


def validate_address(value: str, version: int) -> str:
    addr = ipaddress.ip_address(value)
    if addr.version != version:
        raise ValueError(f"Expected IPv{version}, received IPv{addr.version}")
    return str(addr)


def is_verified(state: object) -> bool:
    # Strict boolean prevents truthy strings like "pending" or "false".
    return state is True


def plan_node(node: dict) -> dict:
    region = node.get("region", "")
    if not isinstance(region, str) or not REGION_RE.fullmatch(region):
        raise ValueError("Invalid region ID")
    region = region.upper()
    checks = node.get("checks", {})
    if not isinstance(checks, dict):
        raise ValueError("checks must be an object")
    if not is_verified(checks.get("linux_tcp_brutal_v2_active")):
        return {"region": region, "candidates": [], "skipped": ["TCP Brutal v2 module not proven active"]}
    if not is_verified(checks.get("server_downlink_brutal_verified")):
        return {"region": region, "candidates": [], "skipped": ["Server downlink not proven to use Brutal"]}
    if not is_verified(checks.get("authenticated_tcp_proxy_verified")):
        return {"region": region, "candidates": [], "skipped": ["TCP proxy protocol not E2E tested"]}
    rate = checks.get("measured_safe_down_mbps")
    if isinstance(rate, bool) or not isinstance(rate, (float, int)) or not 1 <= rate <= 100000:
        return {"region": region, "candidates": [], "skipped": ["Missing measured safe rate"]}
    port = node.get("port")
    if isinstance(port, bool) or not isinstance(port, int) or not 1 <= port <= 65535:
        raise ValueError("Invalid TCP listen port")
    families = node.get("families", {})
    if not isinstance(families, dict):
        raise ValueError("families must be an object")
    family_ok = {}
    addresses = {}
    for f, version in (("v4", 4), ("v6", 6)):
        rec = families.get(f, {})
        if not isinstance(rec, dict):
            raise ValueError("family must be an object")
        if rec.get("address"):
            addresses[f] = validate_address(rec["address"], version)
        family_ok[f] = bool(addresses.get(f)) and is_verified(rec.get("external_tcp_connected")) \
            and is_verified(rec.get("external_https_via_proxy_verified"))
    host = node.get("dual_hostname", "")
    if host and not isinstance(host, str):
        raise ValueError("Invalid hostname")
    family_ok["dual"] = bool(host and family_ok["v4"] and family_ok["v6"]
                             and is_verified(checks.get("dual_dns_a_aaaa_verified"))
                             and is_verified(checks.get("dual_external_https_verified")))
    policies = node.get("egress", {})
    if not isinstance(policies, dict):
        raise ValueError("egress must be an object")
    entries = []
    skipped = []
    for policy in POLICIES:
        if not is_verified(policies.get(policy)):
            skipped.append(f"egress {policy} not verified")
            continue
        for family in FAMILIES:
            if not family_ok[family]:
                skipped.append(f"{policy}/{family} not verified")
                continue
            server = host if family == "dual" else addresses[family]
            entries.append({
                "tag": f"{region}-TCPBrutal-{family}-{policy}",
                "region": region, "protocol": "vless-tcp", "congestion_control": "tcp-brutal-v2",
                "server": server, "port": port, "family": family, "egress": policy,
                "measured_safe_down_mbps": rate,
                "status": "candidate_verified_by_manifest",
            })
    return {"region": region, "candidates": entries, "skipped": skipped}


def plan(manifest: dict) -> dict:
    nodes = manifest.get("nodes")
    if not isinstance(nodes, list):
        raise ValueError("Manifest needs nodes[]")
    result = [plan_node(n) for n in nodes]
    tags = [c["tag"] for n in result for c in n["candidates"]]
    if len(set(tags)) != len(tags):
        raise ValueError("Duplicate matrix tag")
    return {"candidate_count": len(tags), "nodes": result,
            "note": "Planning metadata only: never treat this as a working subscription."}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("manifest", type=Path, help="private on-operator manifest, not for git")
    args = ap.parse_args()
    print(json.dumps(plan(json.loads(args.manifest.read_text(encoding="utf-8"))),
                     indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
