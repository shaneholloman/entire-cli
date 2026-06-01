package cli

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/auth"
	"github.com/entireio/cli/internal/entireclient/contexts"
	"github.com/entireio/cli/internal/entireclient/tokenstore"
)

// makeContextJWT builds a JWT-shaped token (non-"none" alg) carrying the
// given claims, which is all RecordLoginContext needs.
func makeContextJWT(t *testing.T, payloadJSON string) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	return header + "." + enc.EncodeToString([]byte(payloadJSON)) + "." + enc.EncodeToString([]byte("sig"))
}

func TestRunAuthContexts(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("ENTIRE_CONFIG_DIR", cfgDir)
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	var empty bytes.Buffer
	if err := runAuthContexts(&empty); err != nil {
		t.Fatalf("runAuthContexts (empty): %v", err)
	}
	if !strings.Contains(empty.String(), "No login contexts") {
		t.Fatalf("empty listing = %q, want a 'No login contexts' hint", empty.String())
	}

	exp := time.Now().Add(time.Hour).Unix()
	token := makeContextJWT(t, fmt.Sprintf(`{"iss":"https://core.example.com","handle":"alice","exp":%d}`, exp))
	if _, err := auth.RecordLoginContext(token, true); err != nil {
		t.Fatalf("RecordLoginContext: %v", err)
	}

	var out bytes.Buffer
	if err := runAuthContexts(&out); err != nil {
		t.Fatalf("runAuthContexts: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "* core.example.com") {
		t.Fatalf("listing = %q, want current-marked core.example.com", got)
	}
	if !strings.Contains(got, "alice") {
		t.Fatalf("listing = %q, want handle alice", got)
	}
}

// TestRunAuthContextsSurfacesClusterBindings: cluster_contexts entries are
// listed so a user can audit (and then revoke) any host that
// auto-authenticates with a stored context.
func TestRunAuthContextsSurfacesClusterBindings(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("ENTIRE_CONFIG_DIR", cfgDir)
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	exp := time.Now().Add(time.Hour).Unix()
	token := makeContextJWT(t, fmt.Sprintf(`{"iss":"https://core.example.com","handle":"alice","exp":%d}`, exp))
	if _, err := auth.RecordLoginContext(token, true); err != nil {
		t.Fatalf("RecordLoginContext: %v", err)
	}
	if err := contexts.BindCluster(cfgDir, "evil.example.com", "core.example.com"); err != nil {
		t.Fatalf("BindCluster: %v", err)
	}

	var out bytes.Buffer
	if err := runAuthContexts(&out); err != nil {
		t.Fatalf("runAuthContexts: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Cluster bindings") {
		t.Fatalf("listing = %q, want a 'Cluster bindings' section", got)
	}
	if !strings.Contains(got, "evil.example.com") || !strings.Contains(got, "auth unbind") {
		t.Fatalf("listing = %q, want the bound host and an unbind hint", got)
	}
}

// TestRunAuthContextsSurfacesOrphanedBinding: a binding can outlive every
// login context (manual edit, deleted context). The audit path must still
// list it even though there are no contexts to print.
func TestRunAuthContextsSurfacesOrphanedBinding(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("ENTIRE_CONFIG_DIR", cfgDir)
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	// Written directly: BindCluster refuses a binding to a missing context,
	// so an orphan only arises via manual edits or partial cleanup.
	if err := contexts.Save(cfgDir, &contexts.File{
		ClusterContexts: map[string]string{"evil.example.com": "deleted-ctx"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var out bytes.Buffer
	if err := runAuthContexts(&out); err != nil {
		t.Fatalf("runAuthContexts: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "No login contexts") {
		t.Fatalf("listing = %q, want the empty-state hint", got)
	}
	if !strings.Contains(got, "Cluster bindings") || !strings.Contains(got, "evil.example.com") {
		t.Fatalf("listing = %q, want the orphaned binding surfaced", got)
	}
}

// TestUnbindCluster: removing a binding leaves the underlying context
// intact and is idempotent.
func TestUnbindCluster(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("ENTIRE_CONFIG_DIR", cfgDir)
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	exp := time.Now().Add(time.Hour).Unix()
	if _, err := auth.RecordLoginContext(makeContextJWT(t, fmt.Sprintf(`{"iss":"https://core.example.com","handle":"alice","exp":%d}`, exp)), true); err != nil {
		t.Fatalf("RecordLoginContext: %v", err)
	}
	if err := contexts.BindCluster(cfgDir, "evil.example.com", "core.example.com"); err != nil {
		t.Fatalf("BindCluster: %v", err)
	}

	existed, err := auth.UnbindCluster("evil.example.com")
	if err != nil {
		t.Fatalf("UnbindCluster: %v", err)
	}
	if !existed {
		t.Fatal("UnbindCluster reported no binding, want existed=true")
	}

	// Binding gone, context preserved.
	f, err := contexts.Load(cfgDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := f.ClusterContexts["evil.example.com"]; ok {
		t.Fatal("binding still present after unbind")
	}
	if f.Find("core.example.com") == nil {
		t.Fatal("login context must survive unbind")
	}

	// Idempotent: second unbind reports no binding.
	existed, err = auth.UnbindCluster("evil.example.com")
	if err != nil {
		t.Fatalf("UnbindCluster (second): %v", err)
	}
	if existed {
		t.Fatal("second unbind reported existed=true, want false")
	}
}

func TestWarnIfCrossCoreContext(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("ENTIRE_CONFIG_DIR", cfgDir)
	t.Setenv("ENTIRE_AUTH_BASE_URL", "https://auth.example.com")
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	exp := time.Now().Add(time.Hour).Unix()

	// Same core as the configured auth host: no warning.
	sameName, err := auth.RecordLoginContext(makeContextJWT(t, fmt.Sprintf(`{"iss":"https://auth.example.com","handle":"alice","exp":%d}`, exp)), true)
	if err != nil {
		t.Fatalf("record same-core: %v", err)
	}
	var same bytes.Buffer
	warnIfCrossCoreContext(&same, sameName)
	if same.Len() != 0 {
		t.Fatalf("same-core context should not warn, got: %q", same.String())
	}

	// Different core: warns that the control plane won't follow.
	otherName, err := auth.RecordLoginContext(makeContextJWT(t, fmt.Sprintf(`{"iss":"https://other.example.com","handle":"alice","exp":%d}`, exp)), true)
	if err != nil {
		t.Fatalf("record cross-core: %v", err)
	}
	var diff bytes.Buffer
	warnIfCrossCoreContext(&diff, otherName)
	if !strings.Contains(diff.String(), "other.example.com") || !strings.Contains(diff.String(), "control-plane") {
		t.Fatalf("cross-core context should warn, got: %q", diff.String())
	}
}

func TestNoteRemainingLogins(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("ENTIRE_CONFIG_DIR", cfgDir)
	restore := tokenstore.UseFileBackendForTesting(filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(restore)

	// No contexts: silent.
	var empty bytes.Buffer
	noteRemainingLogins(&empty)
	if empty.Len() != 0 {
		t.Fatalf("no remaining contexts should be silent, got %q", empty.String())
	}

	// A surviving context: names it and points at --all.
	exp := time.Now().Add(time.Hour).Unix()
	if _, err := auth.RecordLoginContext(makeContextJWT(t, fmt.Sprintf(`{"iss":"https://core.example.com","handle":"alice","exp":%d}`, exp)), true); err != nil {
		t.Fatalf("record: %v", err)
	}
	var buf bytes.Buffer
	noteRemainingLogins(&buf)
	if !strings.Contains(buf.String(), "core.example.com") || !strings.Contains(buf.String(), "--all") {
		t.Fatalf("expected note naming the context and --all, got %q", buf.String())
	}
}
