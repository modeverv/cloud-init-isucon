package main

import (
	"errors"
	"os"
	"net/http"
	"strconv"
	"time"

	"github.com/oklog/ulid/v2"
)

const nearbyHideAfterComplete = 1000 * time.Millisecond

// 近くに椅子が無い場合の応答遅延(空振りポーリングの抑制)
var emptyNearbyDelay = func() time.Duration {
	v, _ := strconv.Atoi(os.Getenv("EMPTY_NEARBY_DELAY_MS"))
	return time.Duration(v) * time.Millisecond
}()

const (
	initialFare     = 500
	farePerDistance = 100
)

func newID() string {
	return ulid.Make().String()
}

// マンハッタン距離を求める
func calculateDistance(aLatitude, aLongitude, bLatitude, bLongitude int) int {
	return abs(aLatitude-bLatitude) + abs(aLongitude-bLongitude)
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func calculateFare(pickupLatitude, pickupLongitude, destLatitude, destLongitude int) int {
	meteredFare := farePerDistance * calculateDistance(pickupLatitude, pickupLongitude, destLatitude, destLongitude)
	return initialFare + meteredFare
}

type appPostUsersRequest struct {
	Username       string  `json:"username"`
	FirstName      string  `json:"firstname"`
	LastName       string  `json:"lastname"`
	DateOfBirth    string  `json:"date_of_birth"`
	InvitationCode *string `json:"invitation_code"`
}

type appPostUsersResponse struct {
	ID             string `json:"id"`
	InvitationCode string `json:"invitation_code"`
}

func appPostUsers(w http.ResponseWriter, r *http.Request) {
	req := &appPostUsersRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Username == "" || req.FirstName == "" || req.LastName == "" || req.DateOfBirth == "" {
		writeError(w, http.StatusBadRequest, errors.New("required fields(username, firstname, lastname, date_of_birth) are empty"))
		return
	}

	userID := newID()
	accessToken := secureRandomStr(32)
	invitationCode := secureRandomStr(15)
	now := nowTime()

	mu.Lock()
	var inviter *User
	if req.InvitationCode != nil && *req.InvitationCode != "" {
		if st.invCodeCount["INV_"+*req.InvitationCode] >= 3 {
			mu.Unlock()
			writeError(w, http.StatusBadRequest, errors.New("この招待コードは使用できません。"))
			return
		}
		inviter = st.userByInvCode[*req.InvitationCode]
		if inviter == nil {
			mu.Unlock()
			writeError(w, http.StatusBadRequest, errors.New("この招待コードは使用できません。"))
			return
		}
	}

	u := &User{
		ID: userID, Username: req.Username, Firstname: req.FirstName, Lastname: req.LastName,
		DateOfBirth: req.DateOfBirth, AccessToken: accessToken, InvitationCode: invitationCode, CreatedAt: now,
	}
	st.users[userID] = u
	st.userByToken[accessToken] = u
	st.userByInvCode[invitationCode] = u
	enqueue("INSERT INTO users (id, username, firstname, lastname, date_of_birth, access_token, invitation_code, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		userID, req.Username, req.FirstName, req.LastName, req.DateOfBirth, accessToken, invitationCode, now, now)

	addCoupon := func(owner *User, code string, discount int) {
		owner.Coupons = append(owner.Coupons, &Coupon{UserID: owner.ID, Code: code, Discount: discount, CreatedAt: now})
		enqueue("INSERT INTO coupons (user_id, code, discount, created_at) VALUES (?, ?, ?, ?)", owner.ID, code, discount, now)
	}
	addCoupon(u, "CP_NEW2024", 3000)
	if inviter != nil {
		code := "INV_" + *req.InvitationCode
		addCoupon(u, code, 1500)
		st.invCodeCount[code]++
		addCoupon(inviter, "RWD_"+*req.InvitationCode+"_"+strconv.FormatInt(now.UnixMilli(), 10), 1000)
	}
	mu.Unlock()

	http.SetCookie(w, &http.Cookie{Path: "/", Name: "app_session", Value: accessToken})
	writeJSON(w, http.StatusCreated, &appPostUsersResponse{ID: userID, InvitationCode: invitationCode})
}

type appPostPaymentMethodsRequest struct {
	Token string `json:"token"`
}

func appPostPaymentMethods(w http.ResponseWriter, r *http.Request) {
	req := &appPostPaymentMethodsRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, errors.New("token is required but was empty"))
		return
	}
	user := r.Context().Value(keyUser).(*User)

	mu.Lock()
	if user.PaymentToken != "" {
		mu.Unlock()
		writeError(w, http.StatusInternalServerError, errors.New("payment token already registered"))
		return
	}
	user.PaymentToken = req.Token
	enqueue("INSERT INTO payment_tokens (user_id, token, created_at) VALUES (?, ?, ?)", user.ID, req.Token, nowTime())
	mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

type getAppRidesResponse struct {
	Rides []getAppRidesResponseItem `json:"rides"`
}

type getAppRidesResponseItem struct {
	ID                    string                       `json:"id"`
	PickupCoordinate      Coordinate                   `json:"pickup_coordinate"`
	DestinationCoordinate Coordinate                   `json:"destination_coordinate"`
	Chair                 getAppRidesResponseItemChair `json:"chair"`
	Fare                  int                          `json:"fare"`
	Evaluation            int                          `json:"evaluation"`
	RequestedAt           int64                        `json:"requested_at"`
	CompletedAt           int64                        `json:"completed_at"`
}

type getAppRidesResponseItemChair struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

func appGetRides(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(keyUser).(*User)

	items := []getAppRidesResponseItem{}
	mu.Lock()
	for i := len(user.Rides) - 1; i >= 0; i-- {
		ride := user.Rides[i]
		if ride.Status != stCompleted {
			continue
		}
		item := getAppRidesResponseItem{
			ID:                    ride.ID,
			PickupCoordinate:      Coordinate{Latitude: ride.PickupLat, Longitude: ride.PickupLon},
			DestinationCoordinate: Coordinate{Latitude: ride.DestLat, Longitude: ride.DestLon},
			Fare:                  ride.fare(),
			Evaluation:            ride.Evaluation,
			RequestedAt:           ride.CreatedAt.UnixMilli(),
			CompletedAt:           ride.UpdatedAt.UnixMilli(),
		}
		if ch := st.chairs[ride.ChairID]; ch != nil {
			item.Chair.ID = ch.ID
			item.Chair.Name = ch.Name
			item.Chair.Model = ch.Model
			if ch.Owner != nil {
				item.Chair.Owner = ch.Owner.Name
			}
		}
		items = append(items, item)
	}
	mu.Unlock()

	writeJSON(w, http.StatusOK, &getAppRidesResponse{Rides: items})
}

type appPostRidesRequest struct {
	PickupCoordinate      *Coordinate `json:"pickup_coordinate"`
	DestinationCoordinate *Coordinate `json:"destination_coordinate"`
}

type appPostRidesResponse struct {
	RideID string `json:"ride_id"`
	Fare   int    `json:"fare"`
}

// unusedCoupon 次に使われるクーポン。must hold mu
func nextCoupon(u *User, first bool) *Coupon {
	if first {
		for _, c := range u.Coupons {
			if c.Code == "CP_NEW2024" && c.UsedBy == "" {
				return c
			}
		}
	}
	for _, c := range u.Coupons {
		if c.UsedBy == "" {
			return c
		}
	}
	return nil
}

func appPostRides(w http.ResponseWriter, r *http.Request) {
	req := &appPostRidesRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.PickupCoordinate == nil || req.DestinationCoordinate == nil {
		writeError(w, http.StatusBadRequest, errors.New("required fields(pickup_coordinate, destination_coordinate) are empty"))
		return
	}
	user := r.Context().Value(keyUser).(*User)
	rideID := newID()
	now := nowTime()

	mu.Lock()
	for _, rd := range user.Rides {
		if rd.Status != stCompleted {
			mu.Unlock()
			writeError(w, http.StatusConflict, errors.New("ride already exists"))
			return
		}
	}
	ride := &Ride{
		ID: rideID, UserID: user.ID, User: user,
		PickupLat: req.PickupCoordinate.Latitude, PickupLon: req.PickupCoordinate.Longitude,
		DestLat: req.DestinationCoordinate.Latitude, DestLon: req.DestinationCoordinate.Longitude,
		CreatedAt: now, UpdatedAt: now, Status: stMatching, Hist: []string{stMatching},
	}
	user.Rides = append(user.Rides, ride)
	st.rides[rideID] = ride
	enqueue("INSERT INTO rides (id, user_id, pickup_latitude, pickup_longitude, destination_latitude, destination_longitude, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		rideID, user.ID, ride.PickupLat, ride.PickupLon, ride.DestLat, ride.DestLon, now, now)
	enqueue("INSERT INTO ride_statuses (id, ride_id, status, created_at) VALUES (?, ?, ?, ?)", newID(), rideID, stMatching, now)

	if c := nextCoupon(user, len(user.Rides) == 1); c != nil {
		c.UsedBy = rideID
		ride.Discount = c.Discount
		enqueue("UPDATE coupons SET used_by = ? WHERE user_id = ? AND code = ?", rideID, user.ID, c.Code)
	}
	st.waiting = append(st.waiting, ride)
	fare := ride.fare()
	mu.Unlock()

	writeJSON(w, http.StatusAccepted, &appPostRidesResponse{RideID: rideID, Fare: fare})
}

type appPostRidesEstimatedFareRequest struct {
	PickupCoordinate      *Coordinate `json:"pickup_coordinate"`
	DestinationCoordinate *Coordinate `json:"destination_coordinate"`
}

type appPostRidesEstimatedFareResponse struct {
	Fare     int `json:"fare"`
	Discount int `json:"discount"`
}

func appPostRidesEstimatedFare(w http.ResponseWriter, r *http.Request) {
	req := &appPostRidesEstimatedFareRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.PickupCoordinate == nil || req.DestinationCoordinate == nil {
		writeError(w, http.StatusBadRequest, errors.New("required fields(pickup_coordinate, destination_coordinate) are empty"))
		return
	}
	user := r.Context().Value(keyUser).(*User)

	discount := 0
	mu.Lock()
	if c := nextCoupon(user, true); c != nil {
		discount = c.Discount
	}
	mu.Unlock()

	metered := farePerDistance * calculateDistance(req.PickupCoordinate.Latitude, req.PickupCoordinate.Longitude, req.DestinationCoordinate.Latitude, req.DestinationCoordinate.Longitude)
	discounted := initialFare + max(metered-discount, 0)
	writeJSON(w, http.StatusOK, &appPostRidesEstimatedFareResponse{
		Fare:     discounted,
		Discount: initialFare + metered - discounted,
	})
}

type appPostRideEvaluationRequest struct {
	Evaluation int `json:"evaluation"`
}

type appPostRideEvaluationResponse struct {
	CompletedAt int64 `json:"completed_at"`
}

func appPostRideEvaluatation(w http.ResponseWriter, r *http.Request) {
	rideID := r.PathValue("ride_id")
	req := &appPostRideEvaluationRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Evaluation < 1 || req.Evaluation > 5 {
		writeError(w, http.StatusBadRequest, errors.New("evaluation must be between 1 and 5"))
		return
	}

	mu.Lock()
	ride := st.rides[rideID]
	if ride == nil {
		mu.Unlock()
		writeError(w, http.StatusNotFound, errors.New("ride not found"))
		return
	}
	if ride.Status != stArrived {
		mu.Unlock()
		writeError(w, http.StatusBadRequest, errors.New("not arrived yet"))
		return
	}
	if ride.User == nil || ride.User.PaymentToken == "" {
		mu.Unlock()
		writeError(w, http.StatusBadRequest, errors.New("payment token not registered"))
		return
	}
	token := ride.User.PaymentToken
	fare := ride.fare()
	paymentURL := st.paymentURL
	mu.Unlock()

	if err := requestPaymentGatewayPostPayment(r.Context(), paymentURL, token, ride.ID, fare); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	mu.Lock()
	if ride.Status != stArrived {
		mu.Unlock()
		writeError(w, http.StatusBadRequest, errors.New("not arrived yet"))
		return
	}
	now := nowTime()
	ride.Evaluation = req.Evaluation
	ride.UpdatedAt = now
	enqueue("UPDATE rides SET evaluation = ?, updated_at = ? WHERE id = ?", req.Evaluation, now, ride.ID)
	if ch := st.chairs[ride.ChairID]; ch != nil {
		if ride.HasArrived && ride.HasCarrying {
			ch.StatCount++
			ch.StatSum += req.Evaluation
		}
		ch.Completed = append(ch.Completed, ride)
		if ch.Ride == ride {
			ch.Ride = nil
			ch.FreeAt = now
		}
	}
	setStatus(ride, stCompleted)
	mu.Unlock()

	writeJSON(w, http.StatusOK, &appPostRideEvaluationResponse{CompletedAt: now.UnixMilli()})
}

func appGetNotification(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(keyUser).(*User)
	sub := newSubscriber()
	mu.Lock()
	user.sub = sub
	if n := len(user.Rides); n > 0 {
		r := user.Rides[n-1]
		sub.ride = r
		if r.UserSent >= len(r.Hist) {
			r.UserSent = len(r.Hist) - 1
		}
		flushUser(sub, r)
	}
	mu.Unlock()
	serveSSE(w, r, sub, func() {
		if user.sub == sub {
			user.sub = nil
		}
	})
}

type appGetNearbyChairsResponse struct {
	Chairs      []appGetNearbyChairsResponseChair `json:"chairs"`
	RetrievedAt int64                             `json:"retrieved_at"`
}

type appGetNearbyChairsResponseChair struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Model             string     `json:"model"`
	CurrentCoordinate Coordinate `json:"current_coordinate"`
}

func appGetNearbyChairs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	latStr := q.Get("latitude")
	lonStr := q.Get("longitude")
	distanceStr := q.Get("distance")
	if latStr == "" || lonStr == "" {
		writeError(w, http.StatusBadRequest, errors.New("latitude or longitude is empty"))
		return
	}
	lat, err := strconv.Atoi(latStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("latitude is invalid"))
		return
	}
	lon, err := strconv.Atoi(lonStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("longitude is invalid"))
		return
	}
	distance := 50
	if distanceStr != "" {
		distance, err = strconv.Atoi(distanceStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("distance is invalid"))
			return
		}
	}

	chairs := []appGetNearbyChairsResponseChair{}
	hideAfter := time.Now().Add(-nearbyHideAfterComplete)
	mu.Lock()
	for _, ch := range st.chairList {
		if !ch.IsActive || ch.Ride != nil || !ch.HasLoc || ch.FreeAt.After(hideAfter) {
			continue
		}
		if calculateDistance(lat, lon, ch.Lat, ch.Lon) <= distance {
			chairs = append(chairs, appGetNearbyChairsResponseChair{
				ID: ch.ID, Name: ch.Name, Model: ch.Model,
				CurrentCoordinate: Coordinate{Latitude: ch.Lat, Longitude: ch.Lon},
			})
		}
	}
	mu.Unlock()

	if len(chairs) == 0 && emptyNearbyDelay > 0 {
		time.Sleep(emptyNearbyDelay)
	}
	writeJSON(w, http.StatusOK, &appGetNearbyChairsResponse{
		Chairs:      chairs,
		RetrievedAt: time.Now().UnixMilli(),
	})
}

