package main

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"time"
)

// 全状態はメモリ上に保持し、mu で保護する。DBは永続化のために非同期で書き込む。
var mu sync.Mutex

const (
	stMatching  = "MATCHING"
	stEnroute   = "ENROUTE"
	stPickup    = "PICKUP"
	stCarrying  = "CARRYING"
	stArrived   = "ARRIVED"
	stCompleted = "COMPLETED"
)

type Owner struct {
	ID                 string
	Name               string
	AccessToken        string
	ChairRegisterToken string
	CreatedAt          time.Time
	Chairs             []*Chair
}

type Chair struct {
	ID          string
	OwnerID     string
	Owner       *Owner
	Name        string
	Model       string
	Speed       int
	IsActive    bool
	AccessToken string
	CreatedAt   time.Time

	HasLoc    bool
	Lat, Lon  int
	LocAt     time.Time
	TotalDist int

	// Ride 進行中(未完了)のライド
	Ride *Ride
	// LastRide 最後に割り当てられたライド
	LastRide *Ride
	// Completed 完了済みライド(売上計算用)
	Completed []*Ride

	StatCount int
	StatSum   int
	// FreeAt 直近のライド完了時刻(nearby-chairsにはしばらく出さない)
	FreeAt time.Time

	sub *subscriber
}

type User struct {
	ID             string
	Username       string
	Firstname      string
	Lastname       string
	DateOfBirth    string
	AccessToken    string
	InvitationCode string
	CreatedAt      time.Time

	PaymentToken string
	Rides        []*Ride   // created_at 昇順
	Coupons      []*Coupon // created_at 昇順

	sub *subscriber
}

type Coupon struct {
	UserID    string
	Code      string
	Discount  int
	CreatedAt time.Time
	UsedBy    string
}

type Ride struct {
	ID         string
	UserID     string
	User       *User
	ChairID    string
	PickupLat  int
	PickupLon  int
	DestLat    int
	DestLon    int
	Evaluation int
	CreatedAt  time.Time
	UpdatedAt  time.Time

	Status      string
	Discount    int
	HasCarrying bool
	HasArrived  bool

	// Hist 状態履歴。UserSent/ChairSent は通知済みの件数
	Hist      []string
	UserSent  int
	ChairSent int
}

type State struct {
	paymentURL string
	modelSpeed map[string]int

	owners        map[string]*Owner
	ownerByToken  map[string]*Owner
	ownerByRegTok map[string]*Owner
	chairs        map[string]*Chair
	chairList     []*Chair
	chairByToken  map[string]*Chair
	users         map[string]*User
	userByToken   map[string]*User
	userByInvCode map[string]*User
	invCodeCount  map[string]int
	rides         map[string]*Ride
	waiting       []*Ride
}

var st *State

func newState() *State {
	return &State{
		modelSpeed:    map[string]int{},
		owners:        map[string]*Owner{},
		ownerByToken:  map[string]*Owner{},
		ownerByRegTok: map[string]*Owner{},
		chairs:        map[string]*Chair{},
		chairByToken:  map[string]*Chair{},
		users:         map[string]*User{},
		userByToken:   map[string]*User{},
		userByInvCode: map[string]*User{},
		invCodeCount:  map[string]int{},
		rides:         map[string]*Ride{},
	}
}

func nowTime() time.Time {
	return time.Now().Truncate(time.Microsecond)
}

func (r *Ride) fare() int {
	return initialFare + max(farePerDistance*calculateDistance(r.PickupLat, r.PickupLon, r.DestLat, r.DestLon)-r.Discount, 0)
}

func (r *Ride) sale() int {
	return calculateFare(r.PickupLat, r.PickupLon, r.DestLat, r.DestLon)
}

func (c *Chair) stats() appGetNotificationResponseChairStats {
	s := appGetNotificationResponseChairStats{TotalRidesCount: c.StatCount}
	if c.StatCount > 0 {
		s.TotalEvaluationAvg = float64(c.StatSum) / float64(c.StatCount)
	}
	return s
}

// loadState DBから全状態を読み込む
func loadState(ctx context.Context) (*State, error) {
	s := newState()

	if err := db.GetContext(ctx, &s.paymentURL, "SELECT value FROM settings WHERE name = 'payment_gateway_url'"); err != nil {
		return nil, err
	}

	type modelRow struct {
		Name  string `db:"name"`
		Speed int    `db:"speed"`
	}
	var models []modelRow
	if err := db.SelectContext(ctx, &models, "SELECT name, speed FROM chair_models"); err != nil {
		return nil, err
	}
	for _, m := range models {
		s.modelSpeed[m.Name] = m.Speed
	}

	type ownerRow struct {
		ID                 string    `db:"id"`
		Name               string    `db:"name"`
		AccessToken        string    `db:"access_token"`
		ChairRegisterToken string    `db:"chair_register_token"`
		CreatedAt          time.Time `db:"created_at"`
	}
	var owners []ownerRow
	if err := db.SelectContext(ctx, &owners, "SELECT id, name, access_token, chair_register_token, created_at FROM owners"); err != nil {
		return nil, err
	}
	for _, o := range owners {
		ow := &Owner{ID: o.ID, Name: o.Name, AccessToken: o.AccessToken, ChairRegisterToken: o.ChairRegisterToken, CreatedAt: o.CreatedAt}
		s.owners[ow.ID] = ow
		s.ownerByToken[ow.AccessToken] = ow
		s.ownerByRegTok[ow.ChairRegisterToken] = ow
	}

	type chairRow struct {
		ID          string    `db:"id"`
		OwnerID     string    `db:"owner_id"`
		Name        string    `db:"name"`
		Model       string    `db:"model"`
		IsActive    bool      `db:"is_active"`
		AccessToken string    `db:"access_token"`
		CreatedAt   time.Time `db:"created_at"`
	}
	var chairs []chairRow
	if err := db.SelectContext(ctx, &chairs, "SELECT id, owner_id, name, model, is_active, access_token, created_at FROM chairs ORDER BY id"); err != nil {
		return nil, err
	}
	for _, c := range chairs {
		ch := &Chair{ID: c.ID, OwnerID: c.OwnerID, Name: c.Name, Model: c.Model, Speed: s.modelSpeed[c.Model], IsActive: c.IsActive, AccessToken: c.AccessToken, CreatedAt: c.CreatedAt}
		ch.Owner = s.owners[c.OwnerID]
		if ch.Owner != nil {
			ch.Owner.Chairs = append(ch.Owner.Chairs, ch)
		}
		s.chairs[ch.ID] = ch
		s.chairList = append(s.chairList, ch)
		s.chairByToken[ch.AccessToken] = ch
	}

	rows, err := db.QueryContext(ctx, "SELECT chair_id, latitude, longitude, created_at FROM chair_locations ORDER BY chair_id, created_at")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var chairID string
		var lat, lon int
		var createdAt time.Time
		if err := rows.Scan(&chairID, &lat, &lon, &createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		ch := s.chairs[chairID]
		if ch == nil {
			continue
		}
		if ch.HasLoc {
			ch.TotalDist += calculateDistance(ch.Lat, ch.Lon, lat, lon)
		}
		ch.HasLoc = true
		ch.Lat, ch.Lon, ch.LocAt = lat, lon, createdAt
	}
	rows.Close()

	type userRow struct {
		ID             string    `db:"id"`
		Username       string    `db:"username"`
		Firstname      string    `db:"firstname"`
		Lastname       string    `db:"lastname"`
		DateOfBirth    string    `db:"date_of_birth"`
		AccessToken    string    `db:"access_token"`
		InvitationCode string    `db:"invitation_code"`
		CreatedAt      time.Time `db:"created_at"`
	}
	var users []userRow
	if err := db.SelectContext(ctx, &users, "SELECT id, username, firstname, lastname, date_of_birth, access_token, invitation_code, created_at FROM users"); err != nil {
		return nil, err
	}
	for _, u := range users {
		us := &User{ID: u.ID, Username: u.Username, Firstname: u.Firstname, Lastname: u.Lastname, DateOfBirth: u.DateOfBirth, AccessToken: u.AccessToken, InvitationCode: u.InvitationCode, CreatedAt: u.CreatedAt}
		s.users[us.ID] = us
		s.userByToken[us.AccessToken] = us
		s.userByInvCode[us.InvitationCode] = us
	}

	type tokenRow struct {
		UserID string `db:"user_id"`
		Token  string `db:"token"`
	}
	var tokens []tokenRow
	if err := db.SelectContext(ctx, &tokens, "SELECT user_id, token FROM payment_tokens"); err != nil {
		return nil, err
	}
	for _, t := range tokens {
		if u := s.users[t.UserID]; u != nil {
			u.PaymentToken = t.Token
		}
	}

	type couponRow struct {
		UserID    string         `db:"user_id"`
		Code      string         `db:"code"`
		Discount  int            `db:"discount"`
		CreatedAt time.Time      `db:"created_at"`
		UsedBy    sql.NullString `db:"used_by"`
	}
	var coupons []couponRow
	if err := db.SelectContext(ctx, &coupons, "SELECT user_id, code, discount, created_at, used_by FROM coupons ORDER BY created_at, user_id, code"); err != nil {
		return nil, err
	}
	discountByRide := map[string]int{}
	for _, c := range coupons {
		cp := &Coupon{UserID: c.UserID, Code: c.Code, Discount: c.Discount, CreatedAt: c.CreatedAt, UsedBy: c.UsedBy.String}
		if u := s.users[c.UserID]; u != nil {
			u.Coupons = append(u.Coupons, cp)
		}
		if cp.UsedBy != "" {
			discountByRide[cp.UsedBy] = cp.Discount
		}
		if len(cp.Code) > 4 && cp.Code[:4] == "INV_" {
			s.invCodeCount[cp.Code]++
		}
	}

	type rideRow struct {
		ID         string         `db:"id"`
		UserID     string         `db:"user_id"`
		ChairID    sql.NullString `db:"chair_id"`
		PickupLat  int            `db:"pickup_latitude"`
		PickupLon  int            `db:"pickup_longitude"`
		DestLat    int            `db:"destination_latitude"`
		DestLon    int            `db:"destination_longitude"`
		Evaluation sql.NullInt64  `db:"evaluation"`
		CreatedAt  time.Time      `db:"created_at"`
		UpdatedAt  time.Time      `db:"updated_at"`
	}
	var rides []rideRow
	if err := db.SelectContext(ctx, &rides, "SELECT * FROM rides ORDER BY created_at, id"); err != nil {
		return nil, err
	}
	for _, r := range rides {
		rd := &Ride{ID: r.ID, UserID: r.UserID, ChairID: r.ChairID.String, PickupLat: r.PickupLat, PickupLon: r.PickupLon, DestLat: r.DestLat, DestLon: r.DestLon, Evaluation: int(r.Evaluation.Int64), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
		rd.Discount = discountByRide[rd.ID]
		rd.User = s.users[rd.UserID]
		if rd.User != nil {
			rd.User.Rides = append(rd.User.Rides, rd)
		}
		s.rides[rd.ID] = rd
	}

	srows, err := db.QueryContext(ctx, "SELECT ride_id, status FROM ride_statuses ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var rideID, status string
		if err := srows.Scan(&rideID, &status); err != nil {
			srows.Close()
			return nil, err
		}
		rd := s.rides[rideID]
		if rd == nil {
			continue
		}
		rd.Status = status
		rd.Hist = append(rd.Hist, status)
		rd.UserSent = len(rd.Hist)
		rd.ChairSent = len(rd.Hist)
		switch status {
		case stCarrying:
			rd.HasCarrying = true
		case stArrived:
			rd.HasArrived = true
		}
	}
	srows.Close()

	// 椅子毎のライド状態を構築
	sortedRides := make([]*Ride, 0, len(s.rides))
	for _, r := range s.rides {
		sortedRides = append(sortedRides, r)
	}
	sort.Slice(sortedRides, func(i, j int) bool {
		if sortedRides[i].UpdatedAt.Equal(sortedRides[j].UpdatedAt) {
			return sortedRides[i].ID < sortedRides[j].ID
		}
		return sortedRides[i].UpdatedAt.Before(sortedRides[j].UpdatedAt)
	})
	for _, r := range sortedRides {
		if r.ChairID == "" {
			if r.Status == stMatching {
				s.waiting = append(s.waiting, r)
			}
			continue
		}
		ch := s.chairs[r.ChairID]
		if ch == nil {
			continue
		}
		ch.LastRide = r
		if r.Status == stCompleted {
			ch.Completed = append(ch.Completed, r)
			if r.HasArrived && r.HasCarrying {
				ch.StatCount++
				ch.StatSum += r.Evaluation
			}
		} else {
			ch.Ride = r
		}
	}
	sort.Slice(s.waiting, func(i, j int) bool { return s.waiting[i].CreatedAt.Before(s.waiting[j].CreatedAt) })

	return s, nil
}
