package main

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

var db *sqlx.DB

func main() {
	mux := setup()
	slog.Info("Listening on :8080")
	srv := &http.Server{Addr: ":8080", Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func setup() http.Handler {
	host := getenv("ISUCON_DB_HOST", "127.0.0.1")
	port := getenv("ISUCON_DB_PORT", "3306")
	if _, err := strconv.Atoi(port); err != nil {
		panic(fmt.Sprintf("failed to convert DB port number from ISUCON_DB_PORT environment variable into int: %v", err))
	}

	dbConfig := mysql.NewConfig()
	dbConfig.User = getenv("ISUCON_DB_USER", "isucon")
	dbConfig.Passwd = getenv("ISUCON_DB_PASSWORD", "isucon")
	dbConfig.Addr = net.JoinHostPort(host, port)
	dbConfig.Net = "tcp"
	dbConfig.DBName = getenv("ISUCON_DB_NAME", "isuride")
	dbConfig.ParseTime = true
	dbConfig.InterpolateParams = true

	var err error
	for i := 0; i < 60; i++ {
		db, err = sqlx.Connect("mysql", dbConfig.FormatDSN())
		if err == nil {
			break
		}
		slog.Error("db connect failed, retrying", "err", err)
		time.Sleep(time.Second)
	}
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)

	s, err := loadState(context.Background())
	if err != nil {
		panic(err)
	}
	st = s
	startWriters()
	go matchingLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/initialize", postInitialize)

	mux.HandleFunc("POST /api/app/users", appPostUsers)
	mux.Handle("POST /api/app/payment-methods", appAuth(appPostPaymentMethods))
	mux.Handle("GET /api/app/rides", appAuth(appGetRides))
	mux.Handle("POST /api/app/rides", appAuth(appPostRides))
	mux.Handle("POST /api/app/rides/estimated-fare", appAuth(appPostRidesEstimatedFare))
	mux.Handle("POST /api/app/rides/{ride_id}/evaluation", appAuth(appPostRideEvaluatation))
	mux.Handle("GET /api/app/notification", appAuth(appGetNotification))
	mux.Handle("GET /api/app/nearby-chairs", appAuth(appGetNearbyChairs))

	mux.HandleFunc("POST /api/owner/owners", ownerPostOwners)
	mux.Handle("GET /api/owner/sales", ownerAuth(ownerGetSales))
	mux.Handle("GET /api/owner/chairs", ownerAuth(ownerGetChairs))

	mux.HandleFunc("POST /api/chair/chairs", chairPostChairs)
	mux.Handle("POST /api/chair/activity", chairAuth(chairPostActivity))
	mux.Handle("POST /api/chair/coordinate", chairAuth(chairPostCoordinate))
	mux.Handle("GET /api/chair/notification", chairAuth(chairGetNotification))
	mux.Handle("POST /api/chair/rides/{ride_id}/status", chairAuth(chairPostRideStatus))

	mux.HandleFunc("GET /api/internal/matching", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

type postInitializeRequest struct {
	PaymentServer string `json:"payment_server"`
}

type postInitializeResponse struct {
	Language string `json:"language"`
}

func postInitialize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &postInitializeRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	mu.Lock()
	defer mu.Unlock()

	resetWriters()

	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to initialize: %s: %w", string(out), err))
		return
	}

	if _, err := db.ExecContext(ctx, "UPDATE settings SET value = ? WHERE name = 'payment_gateway_url'", req.PaymentServer); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s, err := loadState(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	st = s

	writeJSON(w, http.StatusOK, postInitializeResponse{Language: "go"})
}

type Coordinate struct {
	Latitude  int `json:"latitude"`
	Longitude int `json:"longitude"`
}

func bindJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func writeJSON(w http.ResponseWriter, statusCode int, v interface{}) {
	buf, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(statusCode)
	w.Write(buf)
}

func writeError(w http.ResponseWriter, statusCode int, err error) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(statusCode)
	buf, _ := json.Marshal(map[string]string{"message": err.Error()})
	w.Write(buf)
	if statusCode >= 500 {
		slog.Error("error response wrote", "err", err)
	}
}

func secureRandomStr(b int) string {
	k := make([]byte, b)
	if _, err := crand.Read(k); err != nil {
		panic(err)
	}
	return hex.EncodeToString(k)
}

type ctxKey int

const (
	keyUser ctxKey = iota
	keyOwner
	keyChair
)

func appAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("app_session")
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("app_session cookie is required"))
			return
		}
		mu.Lock()
		u := st.userByToken[c.Value]
		mu.Unlock()
		if u == nil {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("invalid access token"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyUser, u)))
	})
}

func ownerAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("owner_session")
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("owner_session cookie is required"))
			return
		}
		mu.Lock()
		o := st.ownerByToken[c.Value]
		mu.Unlock()
		if o == nil {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("invalid access token"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyOwner, o)))
	})
}

func chairAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("chair_session")
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("chair_session cookie is required"))
			return
		}
		mu.Lock()
		ch := st.chairByToken[c.Value]
		mu.Unlock()
		if ch == nil {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("invalid access token"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyChair, ch)))
	})
}
