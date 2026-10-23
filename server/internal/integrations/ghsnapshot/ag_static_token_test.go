package ghsnapshot

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func agTestPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestAGPatchStaticTokenClientFromEnv(t *testing.T) {
	t.Run("AG patch: no App and a poll token gives an enabled token client", func(t *testing.T) {
		t.Setenv("GITHUB_APP_ID", "")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
		t.Setenv("GITHUB_POLL_TOKEN", "  ghp_secret \n")
		c, err := NewClientFromEnv()
		if err != nil || c == nil || !c.Enabled() {
			t.Fatalf("want an enabled client, got %v, %v", c, err)
		}
		if c.privateKey != nil || c.staticToken != "ghp_secret" {
			t.Fatalf("token client should hold only the trimmed token")
		}
	})

	t.Run("AG patch: an empty or whitespace poll token is unset", func(t *testing.T) {
		t.Setenv("GITHUB_APP_ID", "")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
		for _, v := range []string{"", "   ", "\n\t"} {
			t.Setenv("GITHUB_POLL_TOKEN", v)
			c, err := NewClientFromEnv()
			if c != nil || err != nil || c.Enabled() {
				t.Fatalf("token %q: want a nil, disabled client, got %v, %v", v, c, err)
			}
		}
	})

	t.Run("AG patch: App vars win and keep the poll token for installation 0 only", func(t *testing.T) {
		t.Setenv("GITHUB_APP_ID", "123")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", agTestPEM(t))
		t.Setenv("GITHUB_POLL_TOKEN", "ghp_secret")
		c, err := NewClientFromEnv()
		if err != nil || !c.Enabled() || c.privateKey == nil {
			t.Fatalf("want an App client, got %v, %v", c, err)
		}
		if tok, ok := c.agStaticToken(0); !ok || tok != "ghp_secret" {
			t.Fatalf("installation 0 should use the poll token")
		}
		if _, ok := c.agStaticToken(42); ok {
			t.Fatalf("a real installation must still mint an App token")
		}
	})

	t.Run("AG patch: App vars without a poll token behave as before", func(t *testing.T) {
		t.Setenv("GITHUB_APP_ID", "123")
		t.Setenv("GITHUB_APP_PRIVATE_KEY", agTestPEM(t))
		t.Setenv("GITHUB_POLL_TOKEN", "")
		c, err := NewClientFromEnv()
		if err != nil || !c.Enabled() {
			t.Fatalf("want an App client, got %v, %v", c, err)
		}
		if _, ok := c.agStaticToken(0); ok {
			t.Fatalf("no poll token, so nothing static")
		}
	})
}

func TestAGPatchStaticTokenAuthenticatesGraphQL(t *testing.T) {
	t.Run("AG patch: a token client sends its token on GraphQL and never mints", func(t *testing.T) {
		var minted int32
		var auth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "access_tokens") {
				atomic.AddInt32(&minted, 1)
			}
			auth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
		}))
		defer srv.Close()

		t.Setenv("GITHUB_APP_ID", "")
		t.Setenv("GITHUB_POLL_TOKEN", "ghp_secret")
		c := agStaticTokenClient()
		c.apiBase = srv.URL
		for _, inst := range []int64{0, 7} {
			if _, err := c.graphQL(context.Background(), inst, "query{x}", nil); err != nil {
				t.Fatalf("installation %d: %v", inst, err)
			}
			if auth != "Bearer ghp_secret" {
				t.Fatalf("installation %d: Authorization = %q", inst, auth)
			}
		}
		if minted != 0 {
			t.Fatalf("a token client must not mint installation tokens, minted %d", minted)
		}
	})

	t.Run("AG patch: a rejected token surfaces a status error without the token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		t.Setenv("GITHUB_POLL_TOKEN", "ghp_secret")
		c := agStaticTokenClient()
		c.apiBase = srv.URL
		_, err := c.graphQL(context.Background(), 0, "query{x}", nil)
		if err == nil || strings.Contains(err.Error(), "ghp_secret") {
			t.Fatalf("want an error free of the token, got %v", err)
		}
	})
}
