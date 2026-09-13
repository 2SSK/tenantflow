package identity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestKeycloakProviderRefreshesTokenOn401(t *testing.T) {
	var tokenRequests, userRequests int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/master/protocol/openid-connect/token":
			atomic.AddInt32(&tokenRequests, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, atomic.LoadInt32(&tokenRequests))
		case r.URL.Path == "/admin/realms/tenantflow/users":
			n := atomic.AddInt32(&userRequests, 1)
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"HTTP 401 Unauthorized"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p := NewKeycloakProvider(server.URL, "tenantflow", "admin", "admin")

	_, found, err := p.GetUserByUsername(context.Background(), "demo-good-00001")
	if err != nil {
		t.Fatalf("GetUserByUsername failed after 401 retry: %v", err)
	}
	if found {
		t.Errorf("found = true, want false (server returns no users)")
	}

	// The 401 must trigger exactly one refresh + retry, and the user query
	// must then succeed.
	if got := atomic.LoadInt32(&userRequests); got != 2 {
		t.Errorf("admin requests = %d, want 2 (one 401 + one retry)", got)
	}
	if got := atomic.LoadInt32(&tokenRequests); got != 2 {
		t.Errorf("token fetches = %d, want 2 (initial + refresh after 401)", got)
	}
}

func TestKeycloakProvider401KeepsOnlyOneRetry(t *testing.T) {
	var tokenRequests, userRequests int32

	// The admin endpoint 401s forever (e.g. admin lost their role): the second
	// 401 must surface to the caller — no retry loop.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/master/protocol/openid-connect/token":
			atomic.AddInt32(&tokenRequests, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, atomic.LoadInt32(&tokenRequests))
		case r.URL.Path == "/admin/realms/tenantflow/users":
			atomic.AddInt32(&userRequests, 1)
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"HTTP 401 Unauthorized"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p := NewKeycloakProvider(server.URL, "tenantflow", "admin", "admin")

	if _, _, err := p.GetUserByUsername(context.Background(), "demo-good-00001"); err == nil {
		t.Fatal("expected error on persistent 401, got nil")
	}
	if got := atomic.LoadInt32(&userRequests); got != 2 {
		t.Errorf("admin requests = %d, want exactly 2 (initial + one retry)", got)
	}
	if got := atomic.LoadInt32(&tokenRequests); got != 2 {
		t.Errorf("token fetches = %d, want 2", got)
	}

	// The cache must be empty after the failed pair: the next call refetches
	// (token count rises) instead of reusing a poisoned token.
	before := atomic.LoadInt32(&tokenRequests)
	if _, _, err := p.GetUserByUsername(context.Background(), "demo-good-00001"); err == nil {
		t.Fatal("expected error on persistent 401, got nil")
	}
	if got := atomic.LoadInt32(&tokenRequests); got != before+1 {
		t.Errorf("token fetches after cache-clear = %d, want %d (cache must be invalidated)", got, before+1)
	}
}

func TestKeycloakProviderReusesCachedToken(t *testing.T) {
	var tokenRequests int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/realms/master/protocol/openid-connect/token":
			atomic.AddInt32(&tokenRequests, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, atomic.LoadInt32(&tokenRequests))
		case r.URL.Path == "/admin/realms/tenantflow/users":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p := NewKeycloakProvider(server.URL, "tenantflow", "admin", "admin")

	for i := 0; i < 5; i++ {
		if _, _, err := p.GetUserByUsername(context.Background(), fmt.Sprintf("u%d", i)); err != nil {
			t.Fatalf("call %d failed: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&tokenRequests); got != 1 {
		t.Errorf("token fetches = %d, want 1 (cached token reused)", got)
	}
}
