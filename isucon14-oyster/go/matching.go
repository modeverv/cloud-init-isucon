package main

import (
	"os"
	"sort"
	"strconv"
	"time"
)

func matchingLoop() {
	interval := 30
	if v, err := strconv.Atoi(os.Getenv("ISUCON_MATCHING_INTERVAL_MS")); err == nil && v > 0 {
		interval = v
	}
	t := time.NewTicker(time.Duration(interval) * time.Millisecond)
	for range t.C {
		mu.Lock()
		doMatching()
		mu.Unlock()
	}
}

const urgentWait = 2 * time.Second

type matchPair struct {
	ride  *Ride
	chair *Chair
	cost  int
}

// doMatching 待機中のライドと空いている椅子を、到着時間が短い組から貪欲に割り当てる。must hold mu
func doMatching() {
	if len(st.waiting) == 0 {
		return
	}
	free := make([]*Chair, 0, 64)
	for _, ch := range st.chairList {
		if ch.IsActive && ch.Ride == nil && ch.HasLoc && ch.sub != nil {
			free = append(free, ch)
		}
	}
	if len(free) == 0 {
		return
	}
	usedRide := make(map[*Ride]bool, len(st.waiting))
	usedChair := make(map[*Chair]bool, len(free))
	now := nowTime()

	// 長く待っているライドは古い順に最寄りの椅子を優先的に割り当てる
	urgent := now.Add(-urgentWait)
	for _, r := range st.waiting {
		if !r.CreatedAt.Before(urgent) {
			break
		}
		var best *Chair
		bestCost := 1 << 30
		for _, ch := range free {
			if usedChair[ch] {
				continue
			}
			sp := max(ch.Speed, 1)
			c := (calculateDistance(ch.Lat, ch.Lon, r.PickupLat, r.PickupLon) + sp - 1) / sp
			if c < bestCost {
				best, bestCost = ch, c
			}
		}
		if best == nil {
			break
		}
		usedRide[r] = true
		usedChair[best] = true
		assign(r, best, now)
	}

	pairs := make([]matchPair, 0, len(free)*len(st.waiting))
	for _, r := range st.waiting {
		if usedRide[r] {
			continue
		}
		for _, ch := range free {
			if usedChair[ch] {
				continue
			}
			d := calculateDistance(ch.Lat, ch.Lon, r.PickupLat, r.PickupLon)
			sp := max(ch.Speed, 1)
			pairs = append(pairs, matchPair{ride: r, chair: ch, cost: (d + sp - 1) / sp})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].cost != pairs[j].cost {
			return pairs[i].cost < pairs[j].cost
		}
		return pairs[i].ride.CreatedAt.Before(pairs[j].ride.CreatedAt)
	})
	for _, p := range pairs {
		if usedRide[p.ride] || usedChair[p.chair] {
			continue
		}
		usedRide[p.ride] = true
		usedChair[p.chair] = true
		assign(p.ride, p.chair, now)
	}
	rest := st.waiting[:0]
	for _, r := range st.waiting {
		if !usedRide[r] {
			rest = append(rest, r)
		}
	}
	for i := len(rest); i < len(st.waiting); i++ {
		st.waiting[i] = nil
	}
	st.waiting = rest
}

// must hold mu
func assign(r *Ride, ch *Chair, now time.Time) {
	r.ChairID = ch.ID
	r.UpdatedAt = now
	ch.Ride = r
	ch.LastRide = r
	enqueue("UPDATE rides SET chair_id = ?, updated_at = ? WHERE id = ?", ch.ID, now, r.ID)
	// 椅子へはMATCHINGから通知する
	r.ChairSent = 0
	flushChair(ch.sub, r)
}

const (
	paymentParallel = 1
	paymentStagger  = 3 * time.Millisecond
)
