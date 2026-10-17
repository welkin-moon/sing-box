#!/bin/bash
set -eu
T=$(mktemp -d /run/puk-probe.XXXXXX);trap 'rm -rf "$T"' EXIT
umask 077
P=http://127.0.0.1:18191
curl -fsS --proxy "$P" --connect-timeout 5 --max-time 12 https://www.cloudflare.com/cdn-cgi/trace > "$T/trace"
G=$(curl -sS --proxy "$P" --connect-timeout 5 --max-time 12 -o /dev/null -w '%{http_code}' https://www.google.com/generate_204 || true)
C=$(curl -sS --proxy "$P" --connect-timeout 5 --max-time 12 -o /dev/null -w '%{http_code}' https://chatgpt.com/cdn-cgi/trace || true)
jq -n --arg ip "$(sed -n 's/^ip=//p' "$T/trace")" --arg warp "$(sed -n 's/^warp=//p' "$T/trace")" --arg loc "$(sed -n 's/^loc=//p' "$T/trace")" --arg google "$G" --arg chatgpt "$C" --argjson checked_at "$(date +%s)" '{ip:$ip,warp:$warp,loc:$loc,google:$google,chatgpt:$chatgpt,checked_at:$checked_at,ok:(($warp=="on" or $warp=="plus") and $google=="204" and $chatgpt=="200")}'
