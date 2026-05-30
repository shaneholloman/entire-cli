package auth

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/api"
	"github.com/entireio/cli/internal/entireclient/contexts"
	"github.com/entireio/cli/internal/entireclient/tokenstore"
	"github.com/zalando/go-keyring"
)

// makeJWT builds a three-segment JWT-shaped string with a non-"none" alg
// (so ParseClaims accepts it) and the given payload. The signature segment
// is arbitrary — claims are parsed unverified.
func makeJWT(t *testing.T, payloadJSON string) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := enc.EncodeToString([]byte(payloadJSON))
	return header + "." + payload + "." + enc.EncodeToString([]byte("sig"))
}

func TestRecordLoginContext_WritesContextAndToken(t *testing.T) {
	// Sets ENTIRE_CONFIG_DIR and swaps the keyring backend — process-global
	// state, so this test cannot run in parallel.
	cfgDir := t.TempDir()
	t.Setenv("ENTIRE_CONFIG_DIR", cfgDir)
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	const coreURL = "https://core.example.com"
	const handle = "alice"
	exp := time.Now().Add(2 * time.Hour).Unix()
	token := makeJWT(t, fmt.Sprintf(`{"iss":%q,"handle":%q,"exp":%d}`, coreURL, handle, exp))

	name, err := RecordLoginContext(token)
	if err != nil {
		t.Fatalf("RecordLoginContext: %v", err)
	}
	if name != "core.example.com" {
		t.Fatalf("context name = %q, want core.example.com", name)
	}

	// Context recorded and made current.
	f, err := contexts.Load(cfgDir)
	if err != nil {
		t.Fatalf("load contexts: %v", err)
	}
	if f.CurrentContext != name {
		t.Fatalf("current_context = %q, want %q", f.CurrentContext, name)
	}
	c := f.Find(name)
	if c == nil {
		t.Fatalf("context %q not found", name)
	}
	if c.CoreURL != coreURL || c.Handle != handle {
		t.Fatalf("context = {CoreURL:%q Handle:%q}, want {%q %q}", c.CoreURL, c.Handle, coreURL, handle)
	}
	wantService := tokenstore.CoreKeyringService(coreURL)
	if c.KeychainService != wantService {
		t.Fatalf("KeychainService = %q, want %q", c.KeychainService, wantService)
	}

	// Token stored at the context's keychain slot, decodable with a
	// future expiry.
	encoded, err := tokenstore.Get(wantService, handle)
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	gotToken, expiresAt := tokenstore.DecodeTokenWithExpiration(encoded)
	if gotToken != token {
		t.Fatalf("stored token mismatch")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("stored expiry %s is not in the future", expiresAt)
	}
}

func TestMigrateLegacyLoginContext_SynthesizesContext(t *testing.T) {
	// Mocks the legacy keyring + swaps the new tokenstore + sets the config
	// dir — all process-global, so not parallel.
	keyring.MockInit()
	cfgDir := t.TempDir()
	t.Setenv("ENTIRE_CONFIG_DIR", cfgDir)
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	const coreURL = "https://legacy-core.example.com"
	const handle = "bob"
	exp := time.Now().Add(time.Hour).Unix()
	legacy := makeJWT(t, fmt.Sprintf(`{"iss":%q,"handle":%q,"exp":%d}`, coreURL, handle, exp))

	// Seed only the legacy single-host keyring entry.
	if err := NewStore().SaveToken(api.AuthBaseURL(), legacy); err != nil {
		t.Fatalf("seed legacy token: %v", err)
	}

	migrated, err := MigrateLegacyLoginContext()
	if err != nil {
		t.Fatalf("MigrateLegacyLoginContext: %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to synthesize a context")
	}

	f, err := contexts.Load(cfgDir)
	if err != nil {
		t.Fatalf("load contexts: %v", err)
	}
	if got := f.ContextsForIssuer(coreURL); len(got) != 1 {
		t.Fatalf("contexts for issuer = %d, want 1", len(got))
	}

	// Idempotent: a second call is a no-op now that a context exists.
	migratedAgain, err := MigrateLegacyLoginContext()
	if err != nil {
		t.Fatalf("second MigrateLegacyLoginContext: %v", err)
	}
	if migratedAgain {
		t.Fatal("second migration should be a no-op")
	}
}

func TestLoginTokenForContext(t *testing.T) {
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	c := &contexts.Context{
		Name:            "core.example.com",
		CoreURL:         "https://core.example.com",
		Handle:          "carol",
		KeychainService: tokenstore.CoreKeyringService("https://core.example.com"),
	}
	if err := tokenstore.Set(c.KeychainService, c.Handle, tokenstore.EncodeTokenWithExpiration("the-jwt", 3600)); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	got, err := LoginTokenForContext(c)
	if err != nil {
		t.Fatalf("LoginTokenForContext: %v", err)
	}
	if got != "the-jwt" {
		t.Fatalf("token = %q, want the-jwt", got)
	}

	if _, err := LoginTokenForContext(nil); err == nil {
		t.Fatal("expected error for nil context")
	}
}

func TestRecordLoginContext_RejectsTokenWithoutIssuer(t *testing.T) {
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	token := makeJWT(t, `{"handle":"alice"}`)
	if _, err := RecordLoginContext(token); err == nil {
		t.Fatal("expected error for token without iss claim, got nil")
	}
}
