package main

import (
	"encoding/json"
	"net/http"
)

// SSEで通知を送る。購読者は state の mu 保護下で登録・送信される。
type subscriber struct {
	ch chan []byte
	// ride ユーザー購読の対象ライド(接続時点の最新ライド)
	ride *Ride
}

func newSubscriber() *subscriber {
	return &subscriber{ch: make(chan []byte, 256)}
}

func (s *subscriber) send(v any) {
	if s == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case s.ch <- b:
	default:
		// 詰まっている購読者は諦める(再接続時にスナップショットを送る)
	}
}

// serveSSE は購読者のチャンネルからイベントを流し続ける。onClose はmu取得下で呼ばれる。
func serveSSE(w http.ResponseWriter, r *http.Request, sub *subscriber, onClose func()) {
	defer func() {
		mu.Lock()
		onClose()
		mu.Unlock()
	}()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	ctx := r.Context()
	buf := make([]byte, 0, 1024)
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-sub.ch:
			buf = buf[:0]
			buf = append(buf, "data: "...)
			buf = append(buf, b...)
			buf = append(buf, "\n\n"...)
		drain:
			for {
				select {
				case b2 := <-sub.ch:
					buf = append(buf, "data: "...)
					buf = append(buf, b2...)
					buf = append(buf, "\n\n"...)
				default:
					break drain
				}
			}
			if _, err := w.Write(buf); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

type appGetNotificationResponseData struct {
	RideID                string                           `json:"ride_id"`
	PickupCoordinate      Coordinate                       `json:"pickup_coordinate"`
	DestinationCoordinate Coordinate                       `json:"destination_coordinate"`
	Fare                  int                              `json:"fare"`
	Status                string                           `json:"status"`
	Chair                 *appGetNotificationResponseChair `json:"chair,omitempty"`
	CreatedAt             int64                            `json:"created_at"`
	UpdateAt              int64                            `json:"updated_at"`
}

type appGetNotificationResponseChair struct {
	ID    string                               `json:"id"`
	Name  string                               `json:"name"`
	Model string                               `json:"model"`
	Stats appGetNotificationResponseChairStats `json:"stats"`
}

type appGetNotificationResponseChairStats struct {
	TotalRidesCount    int     `json:"total_rides_count"`
	TotalEvaluationAvg float64 `json:"total_evaluation_avg"`
}

type simpleUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type chairGetNotificationResponseData struct {
	RideID                string     `json:"ride_id"`
	User                  simpleUser `json:"user"`
	PickupCoordinate      Coordinate `json:"pickup_coordinate"`
	DestinationCoordinate Coordinate `json:"destination_coordinate"`
	Status                string     `json:"status"`
}

// must hold mu
func userEvent(r *Ride, status string) *appGetNotificationResponseData {
	d := &appGetNotificationResponseData{
		RideID:                r.ID,
		PickupCoordinate:      Coordinate{Latitude: r.PickupLat, Longitude: r.PickupLon},
		DestinationCoordinate: Coordinate{Latitude: r.DestLat, Longitude: r.DestLon},
		Fare:                  r.fare(),
		Status:                status,
		CreatedAt:             r.CreatedAt.UnixMilli(),
		UpdateAt:              r.UpdatedAt.UnixMilli(),
	}
	if r.ChairID != "" {
		if ch := st.chairs[r.ChairID]; ch != nil {
			d.Chair = &appGetNotificationResponseChair{
				ID:    ch.ID,
				Name:  ch.Name,
				Model: ch.Model,
				Stats: ch.stats(),
			}
		}
	}
	return d
}

// must hold mu
func chairEvent(r *Ride, status string) *chairGetNotificationResponseData {
	d := &chairGetNotificationResponseData{
		RideID:                r.ID,
		PickupCoordinate:      Coordinate{Latitude: r.PickupLat, Longitude: r.PickupLon},
		DestinationCoordinate: Coordinate{Latitude: r.DestLat, Longitude: r.DestLon},
		Status:                status,
	}
	if r.User != nil {
		d.User = simpleUser{ID: r.User.ID, Name: r.User.Firstname + " " + r.User.Lastname}
	}
	return d
}

// setStatus ライドの状態を変更し、DBに記録して通知する。must hold mu
func setStatus(r *Ride, status string) {
	now := nowTime()
	r.Status = status
	switch status {
	case stCarrying:
		r.HasCarrying = true
	case stArrived:
		r.HasArrived = true
	}
	r.Hist = append(r.Hist, status)
	enqueue("INSERT INTO ride_statuses (id, ride_id, status, created_at) VALUES (?, ?, ?, ?)", newID(), r.ID, status, now)
	if r.User != nil && r.User.sub != nil && r.User.sub.ride == r {
		flushUser(r.User.sub, r)
	}
	if r.ChairID != "" {
		if ch := st.chairs[r.ChairID]; ch != nil && ch.sub != nil && ch.LastRide == r {
			flushChair(ch.sub, r)
		}
	}
}

// flushUser 未通知の状態を順番に全て送る。must hold mu
func flushUser(sub *subscriber, r *Ride) {
	for ; r.UserSent < len(r.Hist); r.UserSent++ {
		sub.send(userEvent(r, r.Hist[r.UserSent]))
	}
}

// flushChair 未通知の状態を順番に全て送る。must hold mu
func flushChair(sub *subscriber, r *Ride) {
	for ; r.ChairSent < len(r.Hist); r.ChairSent++ {
		sub.send(chairEvent(r, r.Hist[r.ChairSent]))
	}
}
