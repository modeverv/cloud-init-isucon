#!/bin/bash
# Usage: ab.sh label n   -> runs bench n times, prints scores
cd "$(dirname "$0")"
for i in $(seq 1 ${2:-3}); do ./bench.sh s1 "$1-$i" | grep -o "pass=[a-z]* スコア=[0-9]*\|種別エラー数=.*" | tr '\n' ' '; echo; done
