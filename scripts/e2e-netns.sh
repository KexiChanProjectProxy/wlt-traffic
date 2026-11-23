#!/bin/sh
# End-to-end check of traffic-count in throw-away network namespaces.
# Needs root, iproute2 and python3. Usage: sudo scripts/e2e-netns.sh ./traffic-count
set -eu
BIN=$(readlink -f "$1")
R=tcnt-r C=tcnt-c
WORK=$(mktemp -d)
cleanup() {
	[ -n "${PID:-}" ] && kill "$PID" 2>/dev/null && wait "$PID" 2>/dev/null || true
	ip netns del $R 2>/dev/null || true
	ip netns del $C 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

ip netns add $R
ip netns add $C
ip link add vr netns $R type veth peer name vc netns $C
ip -n $R addr add 10.99.0.1/24 dev vr
ip -n $C addr add 10.99.0.2/24 dev vc
ip -n $R link set lo up; ip -n $R link set vr up
ip -n $C link set lo up; ip -n $C link set vc up
CMAC=$(ip netns exec $C cat /sys/class/net/vc/address)
RMAC=$(ip netns exec $R cat /sys/class/net/vr/address)

cat > "$WORK/config.json" <<JSON
{"interfaces":["vr"],"bind_address":"127.0.0.1:18081","database_path":"$WORK/t.db","flush_interval":1,"timezone":"Asia/Shanghai"}
JSON
ip netns exec $R "$BIN" --config "$WORK/config.json" > "$WORK/log" 2>&1 &
PID=$!
sleep 1

api() { ip netns exec $R python3 -c "import urllib.request,sys;print(urllib.request.urlopen('http://127.0.0.1:18081'+sys.argv[1]).read().decode())" "$1"; }

# Router serves 20 MB to the client and receives 5 MB from it.
ip netns exec $R python3 - <<'PY' &
import socket
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("10.99.0.1",5001)); s.listen(1)
c,_=s.accept(); n=0
while n < 5_000_000:
    b=c.recv(65536)
    if not b: break
    n+=len(b)
c.sendall(b"x"*20_000_000); c.close()
PY
SRV=$!
sleep 0.5
ip netns exec $C python3 - <<'PY'
import socket
s=socket.create_connection(("10.99.0.1",5001)); s.sendall(b"y"*5_000_000)
n=0
while True:
    b=s.recv(65536)
    if not b: break
    n+=len(b)
assert n==20_000_000, n
PY
wait $SRV
sleep 2.5

OUT=$(api "/api/v1/traffic?window=today")
echo "$OUT"
python3 - "$OUT" "$CMAC" "$RMAC" <<'PY'
import json,sys
d=json.loads(sys.argv[1]); cmac, rmac = sys.argv[2], sys.argv[3]
recs={r["mac"]:r for r in d["records"]}
assert rmac not in recs, "router's own MAC must not be counted as a device"
r=recs[cmac]
up, down = r["upload_bytes"], r["download_bytes"]
print(f"client upload={up} download={down}")
assert 5_000_000 <= up < 5_000_000*1.1, up
assert 20_000_000 <= down < 20_000_000*1.1, down
assert r["interface"]=="vr"
PY

# Re-create the interface: the collector must re-attach by itself.
ip -n $R link del vr
ip link add vr netns $R type veth peer name vc netns $C
ip -n $R addr add 10.99.0.1/24 dev vr; ip -n $C addr add 10.99.0.2/24 dev vc
ip -n $R link set vr up; ip -n $C link set vc up
ip netns exec $C ip link set vc address "$CMAC"
sleep 2
ip netns exec $C ping -q -c 20 -i 0.01 -s 1000 10.99.0.1 >/dev/null
sleep 2.5
api "/api/v1/status"
OUT2=$(api "/api/v1/summary?mac=$CMAC")
echo "$OUT2"
python3 - "$OUT" "$OUT2" "$CMAC" <<'PY'
import json,sys
before={r["mac"]:r for r in json.loads(sys.argv[1])["records"]}[sys.argv[3]]
after=json.loads(sys.argv[2])["windows"]["today"]
assert after["upload_bytes"] >= before["upload_bytes"] + 20*1000, (before, after)
print("re-attach after interface re-creation: OK")
PY

kill $PID; wait $PID || true; PID=
cat "$WORK/log"
echo "E2E PASS"
