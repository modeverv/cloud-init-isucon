package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var paymentClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        2000,
		MaxIdleConnsPerHost: 2000,
		IdleConnTimeout:     60 * time.Second,
	},
	Timeout: 10 * time.Second,
}

type paymentGatewayPostPaymentRequest struct {
	Amount int `json:"amount"`
}

var errPaymentFatal = errors.New("payment fatal")

func postPaymentOnce(ctx context.Context, url, token, key string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	res, err := paymentClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	switch res.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: status %d", errPaymentFatal, res.StatusCode)
	}
	return fmt.Errorf("payment status %d", res.StatusCode)
}

// Idempotency-Key にライドIDを使い、同一キーで並列にリトライして早く成功させる。
// 同一キーなので決済は一度しか処理されない。
func requestPaymentGatewayPostPayment(ctx context.Context, paymentGatewayURL string, token string, rideID string, amount int) error {
	b, err := json.Marshal(&paymentGatewayPostPaymentRequest{Amount: amount})
	if err != nil {
		return err
	}
	url := paymentGatewayURL + "/payments"
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	const parallel = paymentParallel
	results := make(chan error, parallel)
	worker := func(delay time.Duration) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				results <- ctx.Err()
				return
			}
		}
		var last error
		for i := 0; i < 20; i++ {
			err := postPaymentOnce(ctx, url, token, rideID, b)
			if err == nil || errors.Is(err, errPaymentFatal) || ctx.Err() != nil {
				results <- err
				return
			}
			last = err
		}
		results <- last
	}
	for i := 0; i < parallel; i++ {
		go worker(time.Duration(i) * paymentStagger)
	}
	var lastErr error
	for i := 0; i < parallel; i++ {
		err := <-results
		if err == nil {
			return nil
		}
		if errors.Is(err, errPaymentFatal) {
			return err
		}
		lastErr = err
	}
	return lastErr
}
