#!/bin/sh
# 単一キューvirtio NICの受信処理(softirq)をRPS/RFSで全CPUへ分散する
set -e
modprobe tls || true
sysctl -q -w net.core.rps_sock_flow_entries=32768
sysctl -q -w net.core.somaxconn=8192
sysctl -q -w net.core.netdev_max_backlog=8192
sysctl -q -w net.ipv4.tcp_max_syn_backlog=8192
sysctl -q -w net.ipv4.ip_local_port_range="10000 65000"
sysctl -q -w net.ipv4.tcp_tw_reuse=1
mask=$(printf '%x' $(( (1 << $(nproc)) - 1 )))
for dev in /sys/class/net/*; do
  [ -e "$dev/device" ] || continue
  for q in "$dev"/queues/rx-*; do
    echo "$mask" > "$q/rps_cpus"
    echo 32768 > "$q/rps_flow_cnt"
  done
  for q in "$dev"/queues/tx-*; do
    echo "$mask" > "$q/xps_cpus" 2>/dev/null || true
  done
done
