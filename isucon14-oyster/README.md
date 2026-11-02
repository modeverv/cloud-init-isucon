# ISUCON14 ローカル練習 (oyster / Claude)

Mac上のMultipass 3台 (各2vCPU/4GiB) + ベンチ専用VM (8vCPU) での ISUCON14 過去問挑戦。

## 結果

| 版 | スコア | 備考 |
|---|---:|---|
| 初期実装 (contest-start) | 524 | lancetfish/isucon14-local/baseline.md |
| codex 最高 | 34,527 | bench VM 上の過去ログ |
| 本実装 (最終) | **190,840 〜 270,097** (全て pass) | 3台構成。同一コードでも±30k程度ぶれる |

## 構成

| VM | 役割 |
|---|---|
| isucon14-1 (s1, 192.168.252.3) | nginx (TLS終端・静的配信) → s3 へ proxy |
| isucon14-3 (s3, 192.168.252.2) | Go アプリ (全状態オンメモリ) |
| isucon14-2 (s2, 192.168.252.4) | MySQL (永続化のみ。非同期書き込み) |

matcher / payment_mock サービスは停止 (マッチングはアプリ内 goroutine)。

## 主な変更

- **全データをメモリ保持**: 起動時/initialize 時に DB から全テーブルを読み込み、以降の参照は全てメモリ。N+1 を含む読み取りクエリはゼロ。
- **DB は非同期書き込み**: 単一 writer で順序を保持、chair_locations はバルク INSERT。再起動後は DB から状態を復元 (再起動テスト済み)。
- **通知を SSE 化**: 状態変化を即時 push。ライドごとに状態履歴を持ち、未送信分を順に再送 (ベンチは前ライドの検証済みイベントを保持し続けるので ENROUTE の欠落が CODE=33 になる)。
- **マッチング**: 30ms 毎。到着時間 (距離/速度) の小さい組から貪欲割当、2秒以上待ったライドは最優先 (30秒タイムアウト防止)。
- **nearby-chairs**: ライド完了直後 1 秒は空車として返さない (ベンチが評価済みを反映する前に返すと CODE=30)。
- **決済**: Idempotency-Key=ライドID で成功までリトライ。並列ヘッジは二重決済になるので不可 (検証済み)。
- nginx: upstream keepalive, SSE 用 proxy_buffering off, worker_connections 増加。MySQL: binlog 無効等。

## わかったこと (このローカル環境固有)

- ボトルネックは **ベンチVM自身の CPU (~97%)**。perf によるとベンチ CPU の 44% は静的ファイルの md5 検証。サーバー側は s1 ~65%, s3 ~50%, s2 ~20%。
- Mac ホスト自体が他プロセスで負荷を受けており (load avg 13〜15)、スコアのぶれが大きい。比較は複数回実行の平均で行う。

## 使い方

```sh
./deploy.sh                          # 既定: NGINX=s1 APP=s1 DB=s2
NGINX=s1 APP=s3 DB=s2 ./deploy.sh    # 最終構成
./bench.sh s1 label                  # 1回計測 (results/ に保存)
./ab.sh label 3                      # 3回計測
```

SSH 設定は `./ssh_config` (lancetfish/isucon14-local/keys の鍵を使用)。
codex の最終状態は各競技VMの snapshot `codex-final-20261102T100853Z` に保存済み。
