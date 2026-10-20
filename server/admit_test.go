package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/lib/guard"
)

func TestRejectCooldownSaysHowLong(t *testing.T) {
	meter := guard.NewCooldownMap()
	ip := "192.0.2.9"
	if !meter.Allow(ip, 5*time.Second) {
		t.Fatal("first Allow refused")
	}
	rec := httptest.NewRecorder()
	rejectCooldown(rec, meter, ip, 5*time.Second, "too many attempts")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	retryAfterSeconds(t, rec)
}

func TestRejectIPBusySaysHowLong(t *testing.T) {
	rec := httptest.NewRecorder()
	rejectIPBusy(rec, "busy")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	retryAfterSeconds(t, rec)
}
