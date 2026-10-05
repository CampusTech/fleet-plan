package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fleet-api-bulk scales to zero and takes ~50s to start, longer than the 30s
// client timeout. The first title-detail requests of a CI run timed out (499
// at the load balancer) and the categories warning blamed token permissions.
// Transient failures must be retried; definitive ones must not.
func TestGetRetriesTransientFailures(t *testing.T) {
	tests := []struct {
		name      string
		responses []int // status per attempt; -1 = hang past the client timeout
		wantCalls int32
		wantErr   bool
	}{
		{name: "timeout then ok", responses: []int{-1, 200}, wantCalls: 2},
		{name: "503 twice then ok", responses: []int{503, 503, 200}, wantCalls: 3},
		{name: "502 then ok", responses: []int{502, 200}, wantCalls: 2},
		{name: "504 then ok", responses: []int{504, 200}, wantCalls: 2},
		{name: "429 then ok", responses: []int{429, 200}, wantCalls: 2},
		{name: "404 not retried", responses: []int{404, 200}, wantCalls: 1, wantErr: true},
		{name: "403 not retried", responses: []int{403, 200}, wantCalls: 1, wantErr: true},
		{name: "500 not retried", responses: []int{500, 200}, wantCalls: 1, wantErr: true},
		{name: "gives up after max attempts", responses: []int{503, 503, 503, 200}, wantCalls: 3, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1) - 1
				status := tt.responses[n]
				if status == -1 {
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
					return
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer ts.Close()

			c := testClient(t, ts, "tok")
			c.httpClient.Timeout = 50 * time.Millisecond
			c.retryBackoff = time.Millisecond

			var dest struct{ OK bool }
			err := c.get(context.Background(), "/x", nil, &dest)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got := calls.Load(); got != tt.wantCalls {
				t.Errorf("calls = %d, want %d", got, tt.wantCalls)
			}
			if !tt.wantErr && !dest.OK {
				t.Error("response not decoded")
			}
		})
	}
}

// A cancelled context stops retrying immediately.
func TestGetRetryStopsOnContextCancel(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := testClient(t, ts, "tok")
	c.retryBackoff = time.Hour // a retry would block the test

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	start := time.Now()
	err := c.get(ctx, "/x", nil, &struct{}{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("retry did not stop on cancel")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

// A Client built without NewClient (zero maxAttempts) still retries.
func TestGetRetryDefaultAttempts(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := testClient(t, ts, "tok")
	c.maxAttempts = 0
	if err := c.get(context.Background(), "/x", nil, &struct{}{}); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}
