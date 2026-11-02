package main

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// DBへの書き込みは非同期に行う。順序を保つため単一goroutineで処理する。

type dbOp struct {
	gen     int64
	query   string
	args    []any
	barrier chan struct{}
}

type locRow struct {
	gen       int64
	id        string
	chairID   string
	lat, lon  int
	createdAt time.Time
	barrier   chan struct{}
}

var (
	dbGen  atomic.Int64
	mainCh = make(chan dbOp, 1<<20)
	locCh  = make(chan locRow, 1<<20)
)

// enqueue must be called while holding mu so that queue order equals state order.
func enqueue(query string, args ...any) {
	mainCh <- dbOp{gen: dbGen.Load(), query: query, args: args}
}

func enqueueLoc(id, chairID string, lat, lon int, createdAt time.Time) {
	locCh <- locRow{gen: dbGen.Load(), id: id, chairID: chairID, lat: lat, lon: lon, createdAt: createdAt}
}

// resetWriters drops all pending writes and waits until writers are idle.
func resetWriters() {
	dbGen.Add(1)
	b1 := make(chan struct{})
	b2 := make(chan struct{})
	mainCh <- dbOp{barrier: b1}
	locCh <- locRow{barrier: b2}
	<-b1
	<-b2
}

func startWriters() {
	go mainWriter()
	go locWriter()
}

func mainWriter() {
	ctx := context.Background()
	for op := range mainCh {
		if op.barrier != nil {
			close(op.barrier)
			continue
		}
		if op.gen != dbGen.Load() {
			continue
		}
		for i := 0; i < 3; i++ {
			if _, err := db.ExecContext(ctx, op.query, op.args...); err != nil {
				slog.Error("db write failed", "err", err, "query", op.query)
				time.Sleep(10 * time.Millisecond)
				continue
			}
			break
		}
	}
}

func locWriter() {
	ctx := context.Background()
	const maxBatch = 500
	batch := make([]locRow, 0, maxBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		var sb strings.Builder
		sb.WriteString("INSERT INTO chair_locations (id, chair_id, latitude, longitude, created_at) VALUES ")
		args := make([]any, 0, len(batch)*5)
		for i, r := range batch {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?,?)")
			args = append(args, r.id, r.chairID, r.lat, r.lon, r.createdAt)
		}
		if _, err := db.ExecContext(ctx, sb.String(), args...); err != nil {
			slog.Error("loc write failed", "err", err)
		}
		batch = batch[:0]
	}
	for r := range locCh {
		if r.barrier != nil {
			batch = batch[:0]
			close(r.barrier)
			continue
		}
		if r.gen != dbGen.Load() {
			continue
		}
		batch = append(batch, r)
	drain:
		for len(batch) < maxBatch {
			select {
			case r2 := <-locCh:
				if r2.barrier != nil {
					batch = batch[:0]
					close(r2.barrier)
					break drain
				}
				if r2.gen == dbGen.Load() {
					batch = append(batch, r2)
				}
			default:
				break drain
			}
		}
		flush()
	}
}
