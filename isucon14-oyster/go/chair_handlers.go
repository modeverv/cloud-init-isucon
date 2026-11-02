package main

import (
	"errors"
	"net/http"
)

type chairPostChairsRequest struct {
	Name               string `json:"name"`
	Model              string `json:"model"`
	ChairRegisterToken string `json:"chair_register_token"`
}

type chairPostChairsResponse struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
}

func chairPostChairs(w http.ResponseWriter, r *http.Request) {
	req := &chairPostChairsRequest{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Model == "" || req.ChairRegisterToken == "" {
		writeError(w, http.StatusBadRequest, errors.New("some of required fields(name, model, chair_register_token) are empty"))
		return
	}

	chairID := newID()
	accessToken := secureRandomStr(32)
	now := nowTime()

	mu.Lock()
	owner := st.ownerByRegTok[req.ChairRegisterToken]
	if owner == nil {
		mu.Unlock()
		writeError(w, http.StatusUnauthorized, errors.New("invalid chair_register_token"))
		return
	}
	ch := &Chair{
		ID: chairID, OwnerID: owner.ID, Owner: owner, Name: req.Name, Model: req.Model,
		Speed: st.modelSpeed[req.Model], AccessToken: accessToken, CreatedAt: now,
	}
	st.chairs[chairID] = ch
	st.chairList = append(st.chairList, ch)
	st.chairByToken[accessToken] = ch
	owner.Chairs = append(owner.Chairs, ch)
	enqueue("INSERT INTO chairs (id, owner_id, name, model, is_active, access_token, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		chairID, owner.ID, req.Name, req.Model, false, accessToken, now, now)
	mu.Unlock()

	http.SetCookie(w, &http.Cookie{Path: "/", Name: "chair_session", Value: accessToken})
	writeJSON(w, http.StatusCreated, &chairPostChairsResponse{ID: chairID, OwnerID: owner.ID})
}

type postChairActivityRequest struct {
	IsActive bool `json:"is_active"`
}

func chairPostActivity(w http.ResponseWriter, r *http.Request) {
	chair := r.Context().Value(keyChair).(*Chair)
	req := &postChairActivityRequest{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mu.Lock()
	chair.IsActive = req.IsActive
	enqueue("UPDATE chairs SET is_active = ?, updated_at = ? WHERE id = ?", req.IsActive, nowTime(), chair.ID)
	mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

type chairPostCoordinateResponse struct {
	RecordedAt int64 `json:"recorded_at"`
}

func chairPostCoordinate(w http.ResponseWriter, r *http.Request) {
	req := &Coordinate{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	chair := r.Context().Value(keyChair).(*Chair)
	now := nowTime()

	mu.Lock()
	if chair.HasLoc {
		chair.TotalDist += calculateDistance(chair.Lat, chair.Lon, req.Latitude, req.Longitude)
	}
	chair.HasLoc = true
	chair.Lat, chair.Lon, chair.LocAt = req.Latitude, req.Longitude, now
	enqueueLoc(newID(), chair.ID, req.Latitude, req.Longitude, now)
	if ride := chair.Ride; ride != nil {
		if ride.Status == stEnroute && req.Latitude == ride.PickupLat && req.Longitude == ride.PickupLon {
			setStatus(ride, stPickup)
		} else if ride.Status == stCarrying && req.Latitude == ride.DestLat && req.Longitude == ride.DestLon {
			setStatus(ride, stArrived)
		}
	}
	mu.Unlock()

	writeJSON(w, http.StatusOK, &chairPostCoordinateResponse{RecordedAt: now.UnixMilli()})
}

func chairGetNotification(w http.ResponseWriter, r *http.Request) {
	chair := r.Context().Value(keyChair).(*Chair)
	sub := newSubscriber()
	mu.Lock()
	chair.sub = sub
	if r := chair.LastRide; r != nil {
		if r.ChairSent >= len(r.Hist) {
			r.ChairSent = len(r.Hist) - 1
		}
		flushChair(sub, r)
	}
	mu.Unlock()
	serveSSE(w, r, sub, func() {
		if chair.sub == sub {
			chair.sub = nil
		}
	})
}

type postChairRidesRideIDStatusRequest struct {
	Status string `json:"status"`
}

func chairPostRideStatus(w http.ResponseWriter, r *http.Request) {
	rideID := r.PathValue("ride_id")
	chair := r.Context().Value(keyChair).(*Chair)
	req := &postChairRidesRideIDStatusRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	mu.Lock()
	ride := st.rides[rideID]
	if ride == nil {
		mu.Unlock()
		writeError(w, http.StatusNotFound, errors.New("ride not found"))
		return
	}
	if ride.ChairID != chair.ID {
		mu.Unlock()
		writeError(w, http.StatusBadRequest, errors.New("not assigned to this ride"))
		return
	}
	switch req.Status {
	case stEnroute:
		setStatus(ride, stEnroute)
	case stCarrying:
		if ride.Status != stPickup {
			mu.Unlock()
			writeError(w, http.StatusBadRequest, errors.New("chair has not arrived yet"))
			return
		}
		setStatus(ride, stCarrying)
	default:
		mu.Unlock()
		writeError(w, http.StatusBadRequest, errors.New("invalid status"))
		return
	}
	mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
