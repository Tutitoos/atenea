package codexcert

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCurrent(t *testing.T) Current {
	t.Helper()
	file := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(file, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	fp, err := FileFingerprint(file, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	atenea, cli, desktop := fp, fp, fp
	atenea.Identifier, cli.Identifier, desktop.Identifier = "atenea", "codex", "com.openai.codex"
	return Current{Commit: "abc", Atenea: atenea, CodexCLI: cli, CodexDesktop: desktop, Machine: Machine{OS: "darwin", OSVersion: "15.0", Architecture: "arm64", HostnameHash: "host"}, AppServerSchemaVersion: "codex-app-server/v2-experimental", AppServerSchemaSHA256: "schema", ProfilesSHA256: "profiles", MCPOverlaySHA256: "overlay", PresentationSHA256: "presentation"}
}

func TestCertificateNeedsEveryRealGateAndExactFingerprint(t *testing.T) {
	current := testCurrent(t)
	cert, _, err := New(30*24*time.Hour, current)
	if err != nil {
		t.Fatal(err)
	}
	cert.Identity, cert.CLI, cert.Desktop = PassedGate("identity"), PassedGate("cli"), PassedGate("desktop")
	cert.Seal()
	if cert.State != Failed {
		t.Fatal("cleanup failure did not fail the certificate")
	}
	cert.CleanupVerified = true
	for _, p := range RequiredProfiles {
		cert.Profiles = append(cert.Profiles, ProfileReceipt{Role: p.Role, RequestedModel: p.Model, ObservedModel: p.Model, RequestedEffort: p.Effort, ObservedEffort: p.Effort, ObservedProvider: "openai", ObservedSource: "app_server_thread_receipt", ThreadHash: "thread", TurnHash: "turn", UsageRevision: 1})
	}
	cert.Seal()
	publicKey, privateKey, keyErr := ed25519.GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	if err := Sign(&cert, privateKey); err != nil {
		t.Fatal(err)
	}
	if got := cert.EffectiveState(time.Now(), current); got != Passed {
		t.Fatalf("state=%s", got)
	}
	drift := current
	drift.CodexCLI.SHA256 = "changed"
	if got := cert.EffectiveState(time.Now(), drift); got != Stale {
		t.Fatalf("drift state=%s", got)
	}
	if got := cert.EffectiveState(cert.ExpiresAt, current); got != Expired {
		t.Fatalf("expiry state=%s", got)
	}
	if err := ValidateReleaseArtifact(cert, cert.CreatedAt.Add(time.Minute), publicKey); err != nil {
		t.Fatal(err)
	}
	cert.Signature = strings.Repeat("A", len(cert.Signature))
	if err := ValidateReleaseArtifact(cert, cert.CreatedAt.Add(time.Minute), publicKey); err == nil {
		t.Fatal("tampered signature accepted")
	}
	if err := Sign(&cert, privateKey); err != nil {
		t.Fatal(err)
	}
	cert.Profiles[0].ObservedEffort = "low"
	if err := ValidateReleaseArtifact(cert, cert.CreatedAt.Add(time.Minute), publicKey); err == nil {
		t.Fatal("mismatched receipt accepted")
	}
}

func TestStorePersistsSanitizedCertificateAndSeparateChallenge(t *testing.T) {
	current := testCurrent(t)
	cert, nonce, err := New(time.Hour, current)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Root: t.TempDir()}
	challenge := Challenge{Nonce: nonce, RunID: "run", WorkflowID: "workflow", InvocationID: "invocation", ResultProof: "proof", ChecklistCount: 1}
	if err := store.SaveChallenge(cert.ID, challenge); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeChallenge(cert.ID, time.Now(), Hash("desktop-process")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeChallenge(cert.ID, time.Now(), Hash("desktop-process")); err == nil {
		t.Fatal("challenge was consumed twice")
	}
	if err := store.Save(cert); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ChallengeHash != Hash(nonce) {
		t.Fatal("challenge hash changed")
	}
	data, err := os.ReadFile(filepath.Join(store.Root, "codex-certificates", cert.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), nonce) {
		t.Fatal("nonce leaked into certificate")
	}
	if err := store.DeleteChallenge(cert.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadChallenge(cert.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("challenge error=%v", err)
	}
}

func TestSigningKeyAndEmbeddedIDFailClosed(t *testing.T) {
	root := t.TempDir()
	keyPath := filepath.Join(root, "key")
	privateKey, publicKey, err := LoadOrCreateSigningKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(privateKey) != ed25519.PrivateKeySize || len(publicKey) != ed25519.PublicKeySize {
		t.Fatal("wrong key sizes")
	}
	info, _ := os.Stat(keyPath)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode=%o", info.Mode().Perm())
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateSigningKey(keyPath); err == nil {
		t.Fatal("permissive key accepted")
	}
	symlink := filepath.Join(root, "symlink-key")
	if err := os.Symlink(keyPath, symlink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateSigningKey(symlink); err == nil {
		t.Fatal("symlink signing key accepted")
	}
	current := testCurrent(t)
	cert, _, err := New(time.Hour, current)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Root: t.TempDir()}
	if err := store.Save(cert); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, "codex-certificates", cert.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		t.Fatal("bad fixture")
	}
	value["certificate_id"] = "../../outside"
	data, _ = json.Marshal(value)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(cert.ID); err == nil {
		t.Fatal("embedded id mismatch accepted")
	}
}

func TestPresentationVerifiersRejectPartialAndAcceptOrderedEvidence(t *testing.T) {
	nonce, run, workflow, invocation, proof := "nonce", "run", "workflow", "invocation", "proof"
	lines := []string{
		`{"type":"item.completed","item":{"id":"message-1","type":"agent_message","text":"**ATENEA · workflow.status** — consulto invocation para verificar actividad y progreso."}}`,
		`{"type":"item.started","item":{"id":"tool-1","type":"mcp_tool_call","server":"atenea","tool":"workflow.status","arguments":{"nonce":"nonce","run_id":"run","workflow_id":"workflow","invocation_id":"invocation","after_cursor":0}}}`,
		"{\"type\":\"item.completed\",\"item\":{\"id\":\"tool-1\",\"type\":\"mcp_tool_call\",\"server\":\"atenea\",\"tool\":\"workflow.status\",\"status\":\"completed\",\"result\":\"proof activity=completed nonce run workflow invocation\\n- [x] **P30.** Certificación Codex\\n**Progreso:** `████████████████████` 100 % · 1/1 puntos completados\"}}",
		"{\"type\":\"item.completed\",\"item\":{\"id\":\"message-2\",\"type\":\"agent_message\",\"text\":\"nonce proof invocation\\n- [x] **P30.** Certificación Codex\\n**Progreso:** `████████████████████` 100 % · 1/1 puntos completados\"}}",
		`{"type":"turn.completed"}`,
	}
	if err := VerifyCLIJSONL([]byte(strings.Join(lines, "\n")), nonce, run, workflow, invocation, proof, 1); err != nil {
		t.Fatal(err)
	}
	withoutFinal := append(append([]string{}, lines[:3]...), lines[4])
	if err := VerifyCLIJSONL([]byte(strings.Join(withoutFinal, "\n")), nonce, run, workflow, invocation, proof, 1); err != nil {
		t.Fatalf("completed MCP result was not accepted as deterministic rendering: %v", err)
	}
	incompletePayload := append([]string{}, lines...)
	incompletePayload[2] = `{"type":"item.completed","item":{"id":"tool-1","type":"mcp_tool_call","server":"atenea","tool":"workflow.status","status":"completed","result":"proof activity=completed nonce run workflow invocation"}}`
	if err := VerifyCLIJSONL([]byte(strings.Join(incompletePayload, "\n")), nonce, run, workflow, invocation, proof, 1); err == nil {
		t.Fatal("agent message rescued an incomplete MCP presentation payload")
	}
	extraTool := append(append([]string{}, lines...), `{"type":"item.started","item":{"id":"extra-tool","type":"mcp_tool_call","server":"other","tool":"other.tool","arguments":{}}}`)
	if err := VerifyCLIJSONL([]byte(strings.Join(extraTool, "\n")), nonce, run, workflow, invocation, proof, 1); err == nil {
		t.Fatal("additional MCP tool call accepted")
	}
	extraNotice := append(append([]string{}, lines...), `{"type":"item.completed","item":{"id":"extra-notice","type":"agent_message","text":"**ATENEA · other.tool** — aviso adicional."}}`)
	if err := VerifyCLIJSONL([]byte(strings.Join(extraNotice, "\n")), nonce, run, workflow, invocation, proof, 1); err == nil {
		t.Fatal("additional ATENEA notice accepted")
	}
	fakeText := `{"type":"item.completed","item":{"id":"message","type":"agent_message","text":"**ATENEA · workflow.status** — consulto invocation para verificar actividad y progreso. proof activity=completed - [x] **P30.** Certificación Codex **Progreso:** ` + "`" + `████████████████████` + "`" + ` 100 % · 1/1 puntos completados"}}`
	if err := VerifyCLIJSONL([]byte(fakeText), nonce, run, workflow, invocation, proof, 1); err == nil {
		t.Fatal("agent text impersonated typed MCP events")
	}
	reconnect := []string{
		`{"type":"item.completed","item":{"id":"message-r1","type":"agent_message","text":"**ATENEA · workflow.status** — reanudo invocation desde cursor 1 sin repetir actividad."}}`,
		`{"type":"item.started","item":{"id":"tool-r","type":"mcp_tool_call","server":"atenea","tool":"workflow.status","arguments":{"nonce":"nonce","run_id":"run","workflow_id":"workflow","invocation_id":"invocation","after_cursor":1}}}`,
		`{"type":"item.completed","item":{"id":"tool-r","type":"mcp_tool_call","status":"completed","result":"proof cursor=1 activity=[] notices=[]"}}`,
		`{"type":"item.completed","item":{"id":"message-r2","type":"agent_message","text":"proof cursor=1"}}`,
		`{"type":"turn.completed"}`,
	}
	if err := VerifyCLIReconnectJSONL([]byte(strings.Join(reconnect, "\n")), nonce, run, workflow, invocation, proof); err != nil {
		t.Fatal(err)
	}
	withoutReconnectFinal := append(append([]string{}, reconnect[:3]...), reconnect[4])
	if err := VerifyCLIReconnectJSONL([]byte(strings.Join(withoutReconnectFinal, "\n")), nonce, run, workflow, invocation, proof); err != nil {
		t.Fatalf("completed reconnect result was not accepted: %v", err)
	}
	reordered := []string{reconnect[3], reconnect[1], reconnect[2], reconnect[0]}
	if err := VerifyCLIReconnectJSONL([]byte(strings.Join(reordered, "\n")), nonce, run, workflow, invocation, proof); err == nil {
		t.Fatal("reordered reconnect evidence accepted")
	}
	replayed := append(append([]string{}, reconnect...), `{"type":"item.completed","item":{"id":"extra","type":"agent_message","text":"- [x] repeated checklist\\nProgreso"}}`)
	if err := VerifyCLIReconnectJSONL([]byte(strings.Join(replayed, "\n")), nonce, run, workflow, invocation, proof); err == nil {
		t.Fatal("replayed reconnect checklist accepted")
	}
	desktopReceipt := Hash("desktop-process")
	desktop := "nonce\nATENEA · codex.certify.challenge\nconsulto invocation para verificar actividad y progreso.\nproof run workflow desktop_process_receipt=" + desktopReceipt + " activity=completed\nP30. Certificación Codex\nProgreso:\n████████████████████\n100 % · 1/1 puntos completados"
	if err := VerifyDesktopText(desktop, nonce, run, workflow, invocation, proof, desktopReceipt, 1); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDesktopText(strings.Replace(desktop, "████████████████████", "██", 1), nonce, run, workflow, invocation, proof, desktopReceipt, 1); err == nil {
		t.Fatal("short bar accepted")
	}
}
