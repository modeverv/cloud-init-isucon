#!/bin/bash
# Usage: bench.sh [target-host s1|s2|s3] [label]
set -uo pipefail
cd "$(dirname "$0")"
CFG=$PWD/ssh_config
T=${1:-s1}; LABEL=${2:-run}
case $T in s1) TIP=192.168.252.3;; s2) TIP=192.168.252.4;; s3) TIP=192.168.252.2;; esac
ID=$(date -u +%Y%m%dT%H%M%SZ)-$LABEL
mkdir -p results/$ID
ssh -F $CFG s1 "sudo truncate -s0 /var/log/nginx/access.log"
ssh -F $CFG bench "cd ~ && /home/ubuntu/bench-binaries/8fea9486c54f3b311b4b5d76f2b87dd2c9c5085637f9fa839ae0e1115374525c/bench run --target https://isuride.xiv.isucon.net --addr $TIP:443 --payment-url http://192.168.252.5:12345 --payment-bind-port 12345 --load-timeout 60 --fail-on-error" > results/$ID/stdout.log 2> results/$ID/stderr.log
echo "exit=$?"
grep -E "スコア|不満|クリティカル|ERROR" results/$ID/stdout.log | tail -8
grep -c "椅子が増加" results/$ID/stdout.log | sed 's/^/chair-increase-events: /'
ssh -F $CFG s1 "sudo cat /var/log/nginx/access.log" > results/$ID/access.log
python3 alp.py results/$ID/access.log | head -4
echo results/$ID
