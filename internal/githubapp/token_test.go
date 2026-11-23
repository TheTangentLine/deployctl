package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

func pkcs1PEM(k *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

// fakeGitHub is the token endpoint. It checks the JWT like GitHub does and
// hands out numbered tokens.
type fakeGitHub struct {
	t        *testing.T
	now      func() time.Time
	calls    atomic.Int32
	status   int    // response status; 0 means 201
	body     string // response body when status is set
	lifetime time.Duration
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := f.calls.Add(1)
	if r.Method != http.MethodPost || r.URL.Path != "/app/installations/42/access_tokens" {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		f.t.Errorf("Accept = %q", r.Header.Get("Accept"))
	}
	f.checkJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))

	if f.status != 0 {
		w.WriteHeader(f.status)
		fmt.Fprint(w, f.body)
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"token":      fmt.Sprintf("ghs_token%d", n),
		"expires_at": f.now().Add(f.lifetime).UTC().Format(time.RFC3339),
	})
}

func (f *fakeGitHub) checkJWT(jwt string) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		f.t.Errorf("JWT has %d parts", len(parts))
		return
	}
	enc := base64.RawURLEncoding
	header, _ := enc.DecodeString(parts[0])
	if string(header) != `{"alg":"RS256","typ":"JWT"}` {
		f.t.Errorf("JWT header = %s", header)
	}
	sig, _ := enc.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&testKey.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		f.t.Errorf("JWT signature does not verify: %v", err)
	}
	raw, _ := enc.DecodeString(parts[1])
	var c struct {
		Iat, Exp int64
		Iss      string
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		f.t.Errorf("JWT claims: %v", err)
	}
	now := f.now().Unix()
	if c.Iss != "123456" || c.Iat > now || c.Exp <= now || c.Exp-c.Iat > 600 {
		f.t.Errorf("JWT claims iss=%s iat=%d exp=%d now=%d", c.Iss, c.Iat, c.Exp, now)
	}
}

// setup returns a TokenSource talking to a fake GitHub, with a clock the
// test can move.
func setup(t *testing.T, gh *fakeGitHub) (*TokenSource, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	gh.t, gh.now = t, func() time.Time { return clock }
	if gh.lifetime == 0 {
		gh.lifetime = time.Hour
	}
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)

	ts, err := New("123456", 42, pkcs1PEM(testKey))
	if err != nil {
		t.Fatal(err)
	}
	ts.BaseURL = srv.URL
	ts.Now = func() time.Time { return clock }
	return ts, &clock
}

func TestToken_FetchesAndCaches(t *testing.T) {
	gh := &fakeGitHub{}
	ts, clock := setup(t, gh)
	ctx := context.Background()

	tok, err := ts.Token(ctx)
	if err != nil || tok != "ghs_token1" {
		t.Fatalf("first token = %q, %v", tok, err)
	}
	*clock = clock.Add(50 * time.Minute) // 10 minutes left: still cached
	if tok, _ := ts.Token(ctx); tok != "ghs_token1" || gh.calls.Load() != 1 {
		t.Fatalf("token = %q after %d calls, want cached ghs_token1", tok, gh.calls.Load())
	}
	*clock = clock.Add(6 * time.Minute) // 4 minutes left: refresh
	if tok, _ := ts.Token(ctx); tok != "ghs_token2" || gh.calls.Load() != 2 {
		t.Fatalf("token = %q after %d calls, want fresh ghs_token2", tok, gh.calls.Load())
	}
}

func TestToken_ConcurrentCallersShareOneRefresh(t *testing.T) {
	gh := &fakeGitHub{}
	ts, _ := setup(t, gh)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ts.Token(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if gh.calls.Load() != 1 {
		t.Fatalf("%d token requests, want 1", gh.calls.Load())
	}
}

func TestToken_Errors(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantRetryable bool
		wantIn        string
	}{
		{"bad key or app ID", 401, `{"message":"A JSON web token could not be decoded"}`, false, "private key"},
		{"wrong installation", 404, `{"message":"Not Found"}`, false, "installation ID"},
		{"forbidden", 403, `{"message":"Resource not accessible by integration"}`, false, "403"},
		{"rate limited (403)", 403, `{"message":"API rate limit exceeded"}`, true, "rate limit"},
		{"rate limited (429)", 429, `{}`, true, "429"},
		{"GitHub down", 502, ``, true, "502"},
		{"garbage response", 201, `not json`, true, "unexpected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _ := setup(t, &fakeGitHub{status: tt.status, body: tt.body})
			_, err := ts.Token(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if steps.IsRetryable(err) != tt.wantRetryable {
				t.Fatalf("retryable = %v, want %v: %v", steps.IsRetryable(err), tt.wantRetryable, err)
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Fatalf("error %q should mention %q", err, tt.wantIn)
			}
		})
	}
}

func TestToken_NetworkErrorIsRetryable(t *testing.T) {
	ts, _ := setup(t, &fakeGitHub{})
	ts.BaseURL = "http://127.0.0.1:1" // nothing listens here
	_, err := ts.Token(context.Background())
	if err == nil || !steps.IsRetryable(err) {
		t.Fatalf("got %v, want a retryable error", err)
	}
}

func TestToken_CancelledContextIsNotRetryable(t *testing.T) {
	ts, _ := setup(t, &fakeGitHub{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ts.Token(ctx)
	if !errors.Is(err, context.Canceled) || steps.IsRetryable(err) {
		t.Fatalf("got %v, want context.Canceled, not retryable", err)
	}
}

func TestNew_Keys(t *testing.T) {
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(testKey)
	tests := []struct {
		name    string
		pem     []byte
		wantErr bool
	}{
		{"PKCS#1 (what GitHub issues)", pkcs1PEM(testKey), false},
		{"PKCS#8", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), false},
		{"not PEM", []byte("hello"), true},
		{"wrong block", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New("123456", 42, tt.pem)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	if _, err := New("", 42, pkcs1PEM(testKey)); err == nil {
		t.Fatal("empty app ID accepted")
	}
	if _, err := New("123456", 0, pkcs1PEM(testKey)); err == nil {
		t.Fatal("zero installation ID accepted")
	}
}

func TestBotIdentity(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"token":"ghs_x","expires_at":"2030-01-01T00:00:00Z"}`)
	})
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Errorf("GET /app must use the App JWT, got %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, `{"slug":"deployctl-test"}`)
	})
	mux.HandleFunc("GET /users/{login}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("login") != "deployctl-test[bot]" {
			t.Errorf("looked up user %q", r.PathValue("login"))
		}
		fmt.Fprint(w, `{"id":987654}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ts, err := New("123456", 42, pkcs1PEM(testKey))
	if err != nil {
		t.Fatal(err)
	}
	ts.BaseURL = srv.URL
	name, email, err := ts.BotIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if name != "deployctl-test[bot]" || email != "987654+deployctl-test[bot]@users.noreply.github.com" {
		t.Fatalf("got %q <%s>", name, email)
	}
}
