#!/bin/bash
# Usage: deploy.sh   (topology via env: NGINX=s1 APP=s1 DB=s2)
set -euo pipefail
cd "$(dirname "$0")"
CFG=$PWD/ssh_config
SSH="ssh -F $CFG"
NGINX=${NGINX:-s1}; APP=${APP:-s1}; DB=${DB:-s2}
ip() { case $1 in s1) echo 192.168.252.3;; s2) echo 192.168.252.4;; s3) echo 192.168.252.2;; esac; }
APP_IP=$(ip $APP); DB_IP=$(ip $DB)
[[ $APP == $NGINX ]] && APP_ADDR=127.0.0.1 || APP_ADDR=$APP_IP
[[ $DB == $APP ]] && DB_ADDR=127.0.0.1 || DB_ADDR=$DB_IP

(cd go && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o isuride .)

for h in s1 s2 s3; do
  $SSH $h "sudo systemctl disable --now isuride-matcher isuride-payment_mock >/dev/null 2>&1 || true"
  if [[ $h == $DB ]]; then
    scp -F $CFG -q conf/mysqld.cnf $h:/tmp/mysqld.cnf
    $SSH $h "sudo install -m644 /tmp/mysqld.cnf /etc/mysql/mysql.conf.d/mysqld.cnf && sudo systemctl enable mysql >/dev/null 2>&1 && sudo systemctl restart mysql"
  else
    $SSH $h "sudo systemctl disable --now mysql >/dev/null 2>&1 || true"
  fi
done

for h in s1 s2 s3; do
  if [[ $h == $APP ]]; then
    $SSH $h "sudo systemctl stop isuride-go"
    scp -F $CFG -q go/isuride $h:/home/isucon/webapp/go/isuride
    $SSH $h "sed -i 's/^ISUCON_DB_HOST=.*/ISUCON_DB_HOST=\"$DB_ADDR\"/' /home/isucon/env.sh && grep -q GOGC /home/isucon/env.sh || echo 'GOGC=400' >> /home/isucon/env.sh; sudo sed -i 's/^Requires=mysql.service/#Requires=mysql.service/' /etc/systemd/system/isuride-go.service; sudo systemctl daemon-reload; sudo systemctl enable --now isuride-go >/dev/null 2>&1; sudo systemctl restart isuride-go"
  else
    $SSH $h "sudo systemctl disable --now isuride-go >/dev/null 2>&1 || true"
  fi
  if [[ $h == $NGINX ]]; then
    sed "s/__APP__/$APP_ADDR/" conf/isuride.conf.tmpl > /tmp/isuride.conf.oyster
    scp -F $CFG -q conf/nginx.conf /tmp/isuride.conf.oyster $h:/tmp/
    $SSH $h "sudo install -m644 /tmp/nginx.conf /etc/nginx/nginx.conf && sudo install -m644 /tmp/isuride.conf.oyster /etc/nginx/sites-available/isuride.conf && sudo nginx -t -q && sudo systemctl enable nginx >/dev/null 2>&1 && sudo systemctl restart nginx"
  else
    $SSH $h "sudo systemctl disable --now nginx >/dev/null 2>&1 || true"
  fi
done
sleep 2
$SSH $NGINX "curl -s -o /dev/null -w 'static %{http_code}\n' https://isuride.xiv.isucon.net/ ; curl -s -w ' api %{http_code}\n' https://isuride.xiv.isucon.net/api/app/notification"
echo "deployed NGINX=$NGINX APP=$APP DB=$DB"
