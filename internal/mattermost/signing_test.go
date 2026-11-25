package mattermost

import (
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	s := NewSigner("k", time.Hour)
	signed := s.Sign(map[string]string{"a": "break", "u": "user1"})
	got, err := s.Verify(signed)
	if err != nil || got["a"] != "break" || got["u"] != "user1" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, ok := got["sig"]; ok {
		t.Error("sig should be stripped")
	}
}

func TestTamperedFieldRejected(t *testing.T) {
	s := NewSigner("k", time.Hour)
	signed := s.Sign(map[string]string{"a": "break", "u": "user1"})
	signed["u"] = "user2"
	if _, err := s.Verify(signed); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestAddedFieldRejected(t *testing.T) {
	s := NewSigner("k", time.Hour)
	signed := s.Sign(map[string]string{"a": "break"})
	signed["admin"] = "true"
	if _, err := s.Verify(signed); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestWrongKeyRejected(t *testing.T) {
	signed := NewSigner("k1", time.Hour).Sign(map[string]string{"a": "out"})
	if _, err := NewSigner("k2", time.Hour).Verify(signed); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestExpiredRejected(t *testing.T) {
	s := NewSigner("k", time.Hour)
	base := time.Now()
	s.now = func() time.Time { return base }
	signed := s.Sign(map[string]string{"a": "out"})
	s.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := s.Verify(signed); err == nil {
		t.Fatal("expected expiry")
	}
}

func TestUnsignedRejected(t *testing.T) {
	if _, err := NewSigner("k", time.Hour).Verify(map[string]string{"a": "out", "ts": "1"}); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestCustomStatusExpired(t *testing.T) {
	now := time.Now()
	if (&CustomStatus{Text: "x"}).Expired(now) {
		t.Error("no expiry should not be expired")
	}
	if !(&CustomStatus{Text: "x", ExpiresAt: now.Add(-time.Minute)}).Expired(now) {
		t.Error("past expiry should be expired")
	}
	if (&CustomStatus{Text: "x", ExpiresAt: now.Add(time.Minute)}).Expired(now) {
		t.Error("future expiry should not be expired")
	}
}
