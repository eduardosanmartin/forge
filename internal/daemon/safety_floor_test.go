package daemon

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"127.0.0.1:0", true},
		{"localhost:8080", true},
		{"LOCALHOST:8080", true},
		{"[::1]:8080", true},
		{"0.0.0.0:8080", false},
		{":8080", false},
		{"[::]:8080", false},
		{"192.168.1.5:8080", false},
		{"example.com:8080", false}, // unresolved hostname: not assumed safe
	}
	for _, c := range cases {
		t.Run(c.addr, func(t *testing.T) {
			if got := isLoopbackAddr(c.addr); got != c.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
			}
		})
	}
}

// TestTransportStartRefusesNonLoopbackWithoutAuthAndTLS exercises the
// RF-7.4/RNF-4.11 safety floor. The check runs before net.Listen, so these
// cases never actually open a socket on a non-loopback address.
func TestTransportStartRefusesNonLoopbackWithoutAuthAndTLS(t *testing.T) {
	cases := []struct {
		name      string
		setAuth   bool
		setTLS    bool
		wantInErr []string
	}{
		{name: "neither configured", wantInErr: []string{"auth token", "TLS certificate"}},
		{name: "only auth configured", setAuth: true, wantInErr: []string{"TLS certificate"}},
		{name: "only TLS configured", setTLS: true, wantInErr: []string{"auth token"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := NewTransport("0.0.0.0:0", &Handler{}, slog.New(slog.DiscardHandler))
			if c.setAuth {
				tr.SetAuth(HashToken("s3cret"))
			}
			if c.setTLS {
				// Paths need not exist: the floor check happens before the
				// cert is loaded, so a non-empty path is enough to pass it.
				tr.SetTLS("cert.pem", "key.pem")
			}
			err := tr.Start(context.Background())
			if err == nil {
				t.Fatal("expected Start to refuse a non-loopback bind, got nil error")
			}
			for _, want := range c.wantInErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}

func TestTransportStartAllowsLoopbackWithNoAuthOrTLS(t *testing.T) {
	tr := NewTransport("127.0.0.1:0", &Handler{}, slog.New(slog.DiscardHandler))
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("loopback bind with no auth/TLS should still work exactly as before RF-7.4: %v", err)
	}
	defer tr.Stop()
	if tr.Addr() == "" {
		t.Error("expected a non-empty listening address")
	}
}
