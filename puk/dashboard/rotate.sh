#!/bin/bash
# One registration attempt, bounded execution, narrow rollback (never restores counters).
set -Eeuo pipefail
umask 077
ID=${1:?request id required}
[[ "$ID" =~ ^[a-zA-Z0-9_-]{16,64}$ ]] || exit 2
D=/var/lib/puk-dashboard/rotations/$ID
mkdir -p "$D"
DB=/usr/local/s-ui/db/s-ui.db
MODE=interface
[[ -f /etc/wireguard/wg-warp.conf ]] || MODE=embedded
fail(){ jq -n --arg error "$1" '{ok:false,error:$error}';exit 1; }
/usr/local/sbin/puk-dashboard-probe > "$D/before.json" || fail 'Existing WARP probe failed; nothing changed'
jq -e '.ok==true' "$D/before.json" >/dev/null || fail 'Existing WARP HTTPS checks failed; nothing changed'
OLD=$(jq -r .ip "$D/before.json")
command -v wgcf >/dev/null || fail 'Registration utility unavailable'
cd "$D"
timeout 40 wgcf register --accept-tos > registration.log 2>&1 || fail 'WARP registration failed; original connection preserved'
timeout 30 wgcf generate > generation.log 2>&1 || fail 'WARP profile generation failed; original connection preserved'
[[ -s wgcf-profile.conf ]] || fail 'Empty generated profile'
value(){ sed -n "s/^$1[[:space:]]*=[[:space:]]*//p" wgcf-profile.conf | head -1; }
KEY=$(value PrivateKey);ADDR=$(value Address);PUB=$(value PublicKey)
[[ -n "$KEY" && -n "$PUB" && "$ADDR" == *:* ]] || fail 'New profile lacks IPv6; original connection preserved'
systemctl is-active --quiet puk-healthcheck.timer && touch restore-health || true
if [[ "$MODE" == interface ]]; then
 cp /etc/wireguard/wg-warp.conf before.conf
 awk -v key="$KEY" -v addr="$ADDR" -v pub="$PUB" '/^PrivateKey[[:space:]]*=/{print "PrivateKey = "key;next} /^Address[[:space:]]*=/{print "Address = "addr;next} /^PublicKey[[:space:]]*=/{print "PublicKey = "pub;next} {print}' before.conf > candidate.conf
 cat > rollback.sh <<EOF
#!/bin/bash
set -eu
[[ -f '$D/committed' ]] && exit 0
cp '$D/before.conf' /etc/wireguard/wg-warp.conf
systemctl restart wg-quick@wg-warp
[[ ! -f '$D/restore-health' ]] || systemctl start puk-healthcheck.timer
touch '$D/rolled-back'
EOF
else
 sqlite3 -readonly "$DB" "SELECT 'UPDATE endpoints SET options=X'''||hex(options)||''' WHERE id='||id||';' FROM endpoints WHERE tag IN ('warp-google','warp-google-v4','warp-google-v6');" > restore.sql
 [[ $(wc -l < restore.sql) == 3 ]] || fail 'Unexpected embedded WARP schema'
 sqlite3 -readonly -json "$DB" "SELECT id,CAST(options AS TEXT) AS options FROM endpoints WHERE tag IN ('warp-google','warp-google-v4','warp-google-v6');" > endpoints.json
 jq --arg key "$KEY" --arg addr "$ADDR" --arg pub "$PUB" 'map(.options=(.options|fromjson|.private_key=$key|.address=($addr|split(",")|map(gsub("^ +| +$";"")))|.peers[0].public_key=$pub|del(.peers[0].reserved)|tojson))' endpoints.json > candidate.json
 jq -r '.[]|"UPDATE endpoints SET options=CAST("+(.options|@json)+" AS BLOB) WHERE id="+(.id|tostring)+";"' candidate.json > candidate.sql
 # JSON double quoting is not SQL escaping: turn each JSON string into a hex BLOB.
 : > candidate.sql
 while IFS= read -r item; do
  I=$(jq -r .id <<< "$item");HEX=$(jq -rj .options <<< "$item" | od -An -v -tx1 | tr -d ' \n')
  printf "UPDATE endpoints SET options=X'%s' WHERE id=%s;\n" "$HEX" "$I" >> candidate.sql
 done < <(jq -c '.[]' candidate.json)
 cat > rollback.sh <<EOF
#!/bin/bash
set -eu
[[ -f '$D/committed' ]] && exit 0
sqlite3 '$DB' < '$D/restore.sql'
systemctl restart s-ui
[[ ! -f '$D/restore-health' ]] || systemctl start puk-healthcheck.timer
touch '$D/rolled-back'
EOF
fi
chmod 700 rollback.sh
UNIT=puk-warp-guard-$ID
systemd-run --quiet --unit="$UNIT" --on-active=180s "$D/rollback.sh"
MUTATED=0
cleanup(){ if [[ "$MUTATED" == 1 && ! -f "$D/committed" ]];then "$D/rollback.sh" >/dev/null 2>&1 || true;fi;systemctl stop "$UNIT.timer" >/dev/null 2>&1 || true;[[ ! -f "$D/restore-health" ]] || systemctl start puk-healthcheck.timer; }
trap cleanup EXIT
systemctl stop puk-healthcheck.timer puk-healthcheck.service || true
MUTATED=1
if [[ "$MODE" == interface ]];then
 cp candidate.conf /etc/wireguard/wg-warp.conf
 systemctl restart wg-quick@wg-warp || fail 'Restart failed; reverting'
else
 sqlite3 "$DB" < candidate.sql
 systemctl restart s-ui || fail 'Restart failed; reverting'
fi
sleep 3
/usr/local/sbin/puk-dashboard-probe > "$D/after.json" || fail 'HTTPS probe failed; reverting'
jq -e '.ok==true' "$D/after.json" >/dev/null || fail 'Google or ChatGPT HTTPS failed; reverting'
NEW=$(jq -r .ip "$D/after.json")
[[ "$OLD" != "$NEW" ]] || fail 'WARP assigned the same IPv4; restored original connection'
# Require IPv6 egress too; preserve v6 availability of existing configurations.
curl -fsS --proxy http://127.0.0.1:18191 --connect-to 'www.google.com:443:[2607:f8b0:400a:809::2004]:443' --max-time 15 'https://www.google.com/generate_204' -o /dev/null || fail 'IPv6 HTTPS failed; reverting'
touch committed
systemctl stop "$UNIT.timer" || true
jq -n --arg old_ip "$OLD" --arg new_ip "$NEW" '{ok:true,changed:true,old_ip:$old_ip,new_ip:$new_ip,google:204,chatgpt:200}'
