package session

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name                   string
		created, seen, rotated time.Time
		valid, rotate          bool
	}{
		{"active", now.Add(-time.Hour), now.Add(-time.Minute), now.Add(-time.Minute), true, false},
		{"idle too long", now.Add(-time.Hour), now.Add(-IdleTimeout - time.Second), now.Add(-time.Hour), false, false},
		{"past max age while active", now.Add(-MaxAge - time.Second), now, now, false, false},
		{"rotation due", now.Add(-time.Hour), now.Add(-time.Minute), now.Add(-RotateEvery - time.Second), true, true},
	}
	for _, tt := range tests {
		valid, rotate := check(tt.created, tt.seen, tt.rotated, now)
		if valid != tt.valid || rotate != tt.rotate {
			t.Errorf("%s: got valid=%v rotate=%v, want valid=%v rotate=%v", tt.name, valid, rotate, tt.valid, tt.rotate)
		}
	}
}

func TestCookie(t *testing.T) {
	for _, target := range []string{"http://example.com/", "https://example.com/"} {
		rec := httptest.NewRecorder()
		setCookie(rec, httptest.NewRequest("GET", target, nil), "id", 0)
		c := rec.Result().Cookies()[0]
		if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("%s: cookie not HttpOnly/SameSite", target)
		}
		if c.Secure != (target == "https://example.com/") {
			t.Errorf("%s: Secure=%v", target, c.Secure)
		}
	}
}
