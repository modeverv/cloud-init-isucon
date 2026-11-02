package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"
)

type ownerPostOwnersRequest struct {
	Name string `json:"name"`
}

type ownerPostOwnersResponse struct {
	ID                 string `json:"id"`
	ChairRegisterToken string `json:"chair_register_token"`
}

func ownerPostOwners(w http.ResponseWriter, r *http.Request) {
	req := &ownerPostOwnersRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, errors.New("some of required fields(name) are empty"))
		return
	}
	ownerID := newID()
	accessToken := secureRandomStr(32)
	chairRegisterToken := secureRandomStr(32)
	now := nowTime()

	mu.Lock()
	o := &Owner{ID: ownerID, Name: req.Name, AccessToken: accessToken, ChairRegisterToken: chairRegisterToken, CreatedAt: now}
	st.owners[ownerID] = o
	st.ownerByToken[accessToken] = o
	st.ownerByRegTok[chairRegisterToken] = o
	enqueue("INSERT INTO owners (id, name, access_token, chair_register_token, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		ownerID, req.Name, accessToken, chairRegisterToken, now, now)
	mu.Unlock()

	http.SetCookie(w, &http.Cookie{Path: "/", Name: "owner_session", Value: accessToken})
	writeJSON(w, http.StatusCreated, &ownerPostOwnersResponse{ID: ownerID, ChairRegisterToken: chairRegisterToken})
}

type chairSales struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Sales int    `json:"sales"`
}

type modelSales struct {
	Model string `json:"model"`
	Sales int    `json:"sales"`
}

type ownerGetSalesResponse struct {
	TotalSales int          `json:"total_sales"`
	Chairs     []chairSales `json:"chairs"`
	Models     []modelSales `json:"models"`
}

func ownerGetSales(w http.ResponseWriter, r *http.Request) {
	since := time.Unix(0, 0)
	until := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if s := r.URL.Query().Get("since"); s != "" {
		parsed, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		since = time.UnixMilli(parsed)
	}
	if s := r.URL.Query().Get("until"); s != "" {
		parsed, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		until = time.UnixMilli(parsed)
	}
	untilEnd := until.Add(999 * time.Microsecond)
	owner := r.Context().Value(keyOwner).(*Owner)

	res := ownerGetSalesResponse{}
	modelSalesByModel := map[string]int{}
	modelOrder := []string{}
	mu.Lock()
	for _, ch := range owner.Chairs {
		sales := 0
		for _, ride := range ch.Completed {
			if ride.UpdatedAt.Before(since) || ride.UpdatedAt.After(untilEnd) {
				continue
			}
			sales += ride.sale()
		}
		res.TotalSales += sales
		res.Chairs = append(res.Chairs, chairSales{ID: ch.ID, Name: ch.Name, Sales: sales})
		if _, ok := modelSalesByModel[ch.Model]; !ok {
			modelOrder = append(modelOrder, ch.Model)
		}
		modelSalesByModel[ch.Model] += sales
	}
	mu.Unlock()

	models := []modelSales{}
	for _, m := range modelOrder {
		models = append(models, modelSales{Model: m, Sales: modelSalesByModel[m]})
	}
	res.Models = models
	writeJSON(w, http.StatusOK, res)
}

type ownerGetChairResponse struct {
	Chairs []ownerGetChairResponseChair `json:"chairs"`
}

type ownerGetChairResponseChair struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	Model                  string `json:"model"`
	Active                 bool   `json:"active"`
	RegisteredAt           int64  `json:"registered_at"`
	TotalDistance          int    `json:"total_distance"`
	TotalDistanceUpdatedAt *int64 `json:"total_distance_updated_at,omitempty"`
}

func ownerGetChairs(w http.ResponseWriter, r *http.Request) {
	owner := r.Context().Value(keyOwner).(*Owner)
	res := ownerGetChairResponse{}
	mu.Lock()
	for _, ch := range owner.Chairs {
		c := ownerGetChairResponseChair{
			ID: ch.ID, Name: ch.Name, Model: ch.Model, Active: ch.IsActive,
			RegisteredAt: ch.CreatedAt.UnixMilli(), TotalDistance: ch.TotalDist,
		}
		if ch.HasLoc {
			t := ch.LocAt.UnixMilli()
			c.TotalDistanceUpdatedAt = &t
		}
		res.Chairs = append(res.Chairs, c)
	}
	mu.Unlock()
	writeJSON(w, http.StatusOK, res)
}
