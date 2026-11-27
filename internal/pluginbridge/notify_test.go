package pluginbridge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNotifierPostsToPlugin(t *testing.T) {
	got := make(chan map[string]string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/plugins/com.bittrix.attendance/internal/notify" || r.Header.Get(SecretHeader) != "s3cret" || r.Method != http.MethodPost {
			t.Errorf("request: %s %s secret=%q", r.Method, r.URL.Path, r.Header.Get(SecretHeader))
		}
		var e map[string]string
		_ = json.NewDecoder(r.Body).Decode(&e)
		got <- e
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := NewNotifier(srv.URL+"/", "s3cret", slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.Start(ctx)
	n.Notify("u1", "session_changed", "teamA")
	select {
	case e := <-got:
		if e["user_id"] != "u1" || e["event"] != "session_changed" || e["team_id"] != "teamA" {
			t.Errorf("event: %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no request")
	}
}

// Notify never blocks, even when nothing sends (queue full).
func TestNotifyNeverBlocks(t *testing.T) {
	n := NewNotifier("http://127.0.0.1:1", "s", slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			n.Notify("u", "e", "")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify blocked")
	}
}
