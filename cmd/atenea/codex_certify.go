package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	adaptercodex "github.com/Tutitoos/atenea/internal/adapter/codex"
	"github.com/Tutitoos/atenea/internal/buildinfo"
	"github.com/Tutitoos/atenea/internal/codexcert"
	"github.com/Tutitoos/atenea/internal/platform"
	"github.com/Tutitoos/atenea/internal/wrap"
)

func cmdCodexCertify(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("codex certify requires start, status, complete, cancel or check")
	}
	switch args[0] {
	case "start":
		return codexCertifyStart(args[1:], out)
	case "status":
		return codexCertifyStatus(args[1:], out, false)
	case "complete":
		return codexCertifyComplete(args[1:], out)
	case "cancel":
		return codexCertifyCancel(args[1:], out)
	case "check":
		return codexCertifyStatus(args[1:], out, true)
	case "export":
		return codexCertifyExport(args[1:], out)
	case "challenge":
		return codexCertifyChallenge(args[1:], out)
	case "mcp":
		return codexCertifyMCP(args[1:], os.Stdin, out)
	default:
		return fmt.Errorf("unknown codex certify command %q", args[0])
	}
}

func codexCertifyStart(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("codex certify start", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	desktop := flags.Bool("desktop", false, "require the Codex Desktop presentation challenge")
	ttlText := flags.String("ttl", "30d", "certificate lifetime, maximum 30d")
	codexPath := flags.String("codex", "codex", "Codex CLI path")
	stateRoot := flags.String("state", platform.StateDir(), "ATENEA state root")
	helper := flags.String("helper", "", "desktop helper path recorded for completion")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*desktop {
		return errors.New("codex certify start requires --desktop")
	}
	ttl, err := parseCertificateTTL(*ttlText)
	if err != nil {
		return err
	}
	resolvedCodex, err := exec.LookPath(*codexPath)
	if err != nil {
		return err
	}
	current, err := codexCertificationCurrent(resolvedCodex)
	if err != nil {
		return err
	}
	cert, nonce, err := codexcert.New(ttl, current)
	if err != nil {
		return err
	}
	resultProof, err := codexcert.NewChallengeToken()
	if err != nil {
		return err
	}
	cert.ChallengeHash = codexcert.Hash(nonce + ":" + resultProof)
	challenge := codexcert.Challenge{Nonce: nonce, RunID: "codex-cert-" + cert.ID[:12], WorkflowID: "workflow-" + cert.ID[:12], InvocationID: "invocation-" + cert.ID[:12], ResultProof: resultProof, ChecklistCount: 1}
	store := codexcert.Store{Root: *stateRoot}
	root := filepath.Join(*stateRoot, "codex-certification", "sandboxes", cert.ID)
	if err := createCertificationSandbox(root); err != nil {
		return err
	}
	if err := store.SaveChallenge(cert.ID, challenge); err != nil {
		_ = removeAndVerify(root)
		return err
	}
	env := isolatedCodexEnvironment(root)
	cleanup := func() error { return removeAndVerify(root) }
	fail := func(cause error) error {
		cert.Identity = codexcert.FailedGate(cause)
		cert.State = codexcert.Failed
		cert.CleanupVerified = cleanup() == nil
		_ = store.DeleteChallenge(cert.ID)
		_ = store.Save(cert)
		return cause
	}
	fmt.Fprintf(out, "Certificado: %s\n", cert.ID)
	fmt.Fprintln(out, "Codex abrirá ahora el inicio de sesión por dispositivo dentro de un CODEX_HOME desechable.")
	loginCtx, stopLogin := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopLogin()
	login := exec.CommandContext(loginCtx, resolvedCodex, "login", "--device-auth")
	login.Env = env
	login.Stdin = os.Stdin
	login.Stdout = out
	login.Stderr = os.Stderr
	if err := login.Run(); err != nil {
		return fail(fmt.Errorf("disposable Codex login failed: %w", err))
	}
	status := exec.Command(resolvedCodex, "login", "status")
	status.Env = env
	if data, err := status.CombinedOutput(); err != nil || !isCodexLoggedInStatus(string(data)) {
		if err == nil {
			err = errors.New(strings.TrimSpace(string(data)))
		}
		return fail(fmt.Errorf("disposable Codex authentication was not confirmed: %w", err))
	}
	ctx, cancel := context.WithTimeout(loginCtx, 8*time.Minute)
	defer cancel()
	client, err := adaptercodex.NewAppServerClient(adaptercodex.AppServerOptions{Binary: resolvedCodex, Environment: env, IsolateAmbientHooks: true, TrustAteneaHook: true})
	if err != nil {
		return fail(err)
	}
	receipts, identityErr := codexcert.RunIdentityGate(ctx, client, root)
	closeErr := client.Close()
	if identityErr != nil {
		return fail(identityErr)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	cert.Profiles = receipts
	receiptData, _ := json.Marshal(receipts)
	cert.Identity = codexcert.PassedGate("app_server_thread_receipt:" + codexcert.Hash(string(receiptData)))
	cursorPath, cursorErr := store.CursorPath(cert.ID)
	if cursorErr != nil {
		return fail(cursorErr)
	}
	cliRaw, reconnectRaw, cliErr := runCodexCLIGate(ctx, resolvedCodex, env, challenge, cursorPath)
	if cliErr == nil {
		cliErr = codexcert.VerifyCLIJSONL(cliRaw, challenge.Nonce, challenge.RunID, challenge.WorkflowID, challenge.InvocationID, challenge.ResultProof, challenge.ChecklistCount)
	}
	if cliErr == nil {
		cliErr = codexcert.VerifyCLIReconnectJSONL(reconnectRaw, challenge.Nonce, challenge.RunID, challenge.WorkflowID, challenge.InvocationID, challenge.ResultProof)
	}
	if cliErr != nil {
		cert.CLI = codexcert.FailedGate(cliErr)
		cert.State = codexcert.Failed
	} else {
		cert.CLI = codexcert.PassedGate("codex_cli_jsonl_sha256:" + codexcert.Hash(string(cliRaw)+"\x00"+string(reconnectRaw)))
	}
	if err := cleanup(); err != nil {
		cert.CleanupVerified = false
		cert.State = codexcert.Failed
		if cliErr == nil {
			cliErr = fmt.Errorf("disposable authentication cleanup failed: %w", err)
		}
	} else {
		cert.CleanupVerified = true
	}
	cert.Seal()
	if err := store.Save(cert); err != nil {
		_ = store.DeleteChallenge(cert.ID)
		return err
	}
	if cliErr != nil {
		_ = store.DeleteChallenge(cert.ID)
		return cliErr
	}
	_ = helper // completion resolves the helper afresh; paths never enter certificates.
	self, _ := os.Executable()
	fmt.Fprintf(out, "\nCertificado parcial: %s\n\nPega este texto en un chat nuevo de Codex Desktop:\n\n%s\n", cert.ID, desktopChallengePrompt(cert.ID, challenge, self, *stateRoot))
	return nil
}

func runCodexCLIGate(ctx context.Context, codexPath string, env []string, challenge codexcert.Challenge, cursorPath string) ([]byte, []byte, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	self, _ = filepath.EvalSymlinks(self)
	overlay, err := (wrap.Plan{}).ClientOverlay("codex", wrap.Core{ID: "atenea", Command: []string{self, "codex", "certify", "mcp", "--nonce", challenge.Nonce, "--run", challenge.RunID, "--workflow", challenge.WorkflowID, "--invocation", challenge.InvocationID, "--proof", challenge.ResultProof, "--cursor-state", cursorPath}})
	if err != nil {
		return nil, nil, err
	}
	run := func(prompt string) ([]byte, error) {
		commandArgs := []string{"exec", "--json", "--ephemeral", "--ignore-user-config", "--sandbox", "read-only", "-m", "gpt-5.6-sol", "-c", `model_reasoning_effort="medium"`}
		commandArgs = append(commandArgs, overlay.Args...)
		// The certification MCP exposes one deterministic read-only tool. Codex
		// otherwise refuses it under exec's non-interactive approval policy.
		commandArgs = append(commandArgs, "-c", `mcp_servers.atenea.default_tools_approval_mode="approve"`)
		commandArgs = append(commandArgs, prompt)
		cmd := exec.CommandContext(ctx, codexPath, commandArgs...)
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("codex CLI gate failed: %w: %s", err, bounded(stderr.String(), 1000))
		}
		if stdout.Len() > 4<<20 {
			return nil, errors.New("codex CLI transcript exceeded 4 MiB")
		}
		return bytes.Clone(stdout.Bytes()), nil
	}
	first, err := run(cliChallengePrompt(challenge))
	if err != nil {
		return nil, nil, err
	}
	reconnected, err := run(cliReconnectPrompt(challenge))
	if err != nil {
		return nil, nil, err
	}
	return first, reconnected, nil
}

func cliChallengePrompt(c codexcert.Challenge) string {
	return fmt.Sprintf("Certification challenge %s. Run ATENEA tool workflow.status exactly once with nonce=%s run_id=%s workflow_id=%s invocation_id=%s after_cursor=0. Immediately before the tool call, output exactly one line: **ATENEA · workflow.status** — consulto %s para verificar actividad y progreso. The completed MCP JSONL result is the canonical Markdown render; finish the turn without repeating it. Do not call any other tool.", c.Nonce, c.Nonce, c.RunID, c.WorkflowID, c.InvocationID, c.InvocationID)
}

func cliReconnectPrompt(c codexcert.Challenge) string {
	return fmt.Sprintf("Reconnect challenge %s. Run ATENEA tool workflow.status exactly once with nonce=%s run_id=%s workflow_id=%s invocation_id=%s after_cursor=1. Before the call output exactly one line: **ATENEA · workflow.status** — reanudo %s desde cursor 1 sin repetir actividad. The completed MCP JSONL result is canonical; finish without repeating it or a checklist. Do not call another tool.", c.Nonce, c.Nonce, c.RunID, c.WorkflowID, c.InvocationID, c.InvocationID)
}

func desktopChallengePrompt(certificateID string, c codexcert.Challenge, binary, stateRoot string) string {
	command := fmt.Sprintf("%s codex certify challenge %s --nonce %s --state %s", shellQuote(binary), certificateID, shellQuote(c.Nonce), shellQuote(stateRoot))
	return fmt.Sprintf("Certification challenge %s. Use the shell tool exactly once to run the following command:\n%s\nImmediately before that call, output one Markdown line whose complete bold content is formed by ATENEA, one space, a middle dot, one space, and the exact tool name codex.certify.challenge. After the bold content write an em dash, the Spanish action consulto, the invocation id %s, and the purpose para verificar actividad y progreso. Do not format the tool or invocation as inline code. After success, start the final answer with that same constructed Markdown line because Codex Desktop collapses intermediate activity, then reproduce the returned output literally and completely from nonce= through the Progreso line, including run, workflow, invocation, result_proof, desktop_process_receipt and activity. Do not call another tool.", c.Nonce, command, c.InvocationID)
}

func codexCertifyChallenge(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("codex certify challenge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateRoot := flags.String("state", platform.StateDir(), "ATENEA state root")
	nonce := flags.String("nonce", "", "challenge nonce")
	args = flagsBeforeID(args, map[string]bool{"--state": true, "--nonce": true})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 || *nonce == "" {
		return errors.New("codex certify challenge requires certificate id and nonce")
	}
	certificateID := flags.Arg(0)
	cert, err := codexcert.Store{Root: *stateRoot}.Load(certificateID)
	if err != nil {
		return err
	}
	challenge, err := codexcert.Store{Root: *stateRoot}.LoadChallenge(cert.ID)
	if err != nil {
		return err
	}
	if cert.State != codexcert.Pending || !time.Now().UTC().Before(cert.ExpiresAt) || challenge.Nonce != *nonce || cert.ChallengeHash != codexcert.Hash(challenge.Nonce+":"+challenge.ResultProof) {
		return errors.New("certification challenge does not match")
	}
	if cert.Identity.State != codexcert.Passed || cert.CLI.State != codexcert.Passed || !cert.CleanupVerified {
		return errors.New("certification challenge is not ready for Desktop")
	}
	processReceipt, err := codexDesktopProcessReceipt()
	if err != nil {
		return err
	}
	challenge, err = (codexcert.Store{Root: *stateRoot}).ConsumeChallenge(cert.ID, time.Now().UTC(), processReceipt)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "nonce=%s\nrun=%s\nworkflow=%s\ninvocation=%s\nresult_proof=%s\ndesktop_process_receipt=%s\nactivity=completed\n\n- [x] **P30.** Certificación Codex\n\n**Progreso:** `████████████████████` 100 %% · 1/1 puntos completados\n", challenge.Nonce, challenge.RunID, challenge.WorkflowID, challenge.InvocationID, challenge.ResultProof, challenge.DesktopProcessSHA256)
	return nil
}

func codexCertifyMCP(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("codex certify mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	nonce, runID, workflowID, invocationID := flags.String("nonce", "", ""), flags.String("run", "", ""), flags.String("workflow", "", ""), flags.String("invocation", "", "")
	resultProof := flags.String("proof", "", "")
	cursorState := flags.String("cursor-state", "", "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	c := codexcert.Challenge{Nonce: *nonce, RunID: *runID, WorkflowID: *workflowID, InvocationID: *invocationID, ResultProof: *resultProof, ChecklistCount: 1}
	if c.Nonce == "" || c.RunID == "" || c.WorkflowID == "" || c.InvocationID == "" || c.ResultProof == "" || *cursorState == "" {
		return errors.New("incomplete certification MCP challenge")
	}
	return codexcert.ServeMCP(in, out, c, *cursorState)
}

func codexCertifyComplete(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("codex certify complete", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateRoot := flags.String("state", platform.StateDir(), "ATENEA state root")
	helper := flags.String("helper", "", "desktop helper path")
	codexPath := flags.String("codex", "codex", "Codex CLI path")
	args = flagsBeforeID(args, map[string]bool{"--state": true, "--helper": true, "--codex": true})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("codex certify complete requires CERTIFICATE_ID")
	}
	store := codexcert.Store{Root: *stateRoot}
	cert, err := store.Load(flags.Arg(0))
	if err != nil {
		return err
	}
	challenge, err := store.LoadChallenge(cert.ID)
	if err != nil {
		return err
	}
	if cert.Identity.State != codexcert.Passed || cert.CLI.State != codexcert.Passed || !cert.CleanupVerified {
		return errors.New("identity, CLI and cleanup gates must pass before Desktop completion")
	}
	_, processHashErr := hex.DecodeString(challenge.DesktopProcessSHA256)
	if challenge.UsedAt.IsZero() || challenge.UsedAt.Before(cert.CreatedAt) || challenge.UsedAt.After(time.Now().UTC()) || len(challenge.DesktopProcessSHA256) != 64 || processHashErr != nil {
		return errors.New("desktop challenge has not been consumed by the visible chat")
	}
	resolvedCodex, resolveErr := exec.LookPath(*codexPath)
	if resolveErr != nil {
		return resolveErr
	}
	current, currentErr := codexCertificationCurrent(resolvedCodex)
	if currentErr != nil {
		return currentErr
	}
	if state := cert.EffectiveState(time.Now().UTC(), current); state != codexcert.Pending {
		return fmt.Errorf("codex certificate is %s before Desktop completion", state)
	}
	text, err := inspectCodexDesktop(*helper)
	if err == nil {
		err = codexcert.VerifyDesktopText(text, challenge.Nonce, challenge.RunID, challenge.WorkflowID, challenge.InvocationID, challenge.ResultProof, challenge.DesktopProcessSHA256, challenge.ChecklistCount)
	}
	if err != nil {
		cert.Desktop = codexcert.PendingGate(err)
		cert.Seal()
		_ = store.Save(cert)
		return err
	}
	cert.Desktop = codexcert.PassedGate("desktop_ax_process_sha256:" + codexcert.Hash(challenge.Nonce+":"+challenge.ResultProof+":"+challenge.DesktopProcessSHA256))
	cert.Seal()
	if state := cert.EffectiveState(time.Now().UTC(), current); state != codexcert.Passed {
		cert.State = state
		_ = store.Save(cert)
		return fmt.Errorf("codex certificate became %s before completion", state)
	}
	if err := store.Save(cert); err != nil {
		return err
	}
	if err := store.DeleteChallenge(cert.ID); err != nil {
		return err
	}
	fmt.Fprintf(out, "Codex certificado 100 %%\nID: %s\nVálido hasta: %s\n", cert.ID, cert.ExpiresAt.Format(time.RFC3339))
	return nil
}

func codexCertifyCancel(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("codex certify cancel", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateRoot := flags.String("state", platform.StateDir(), "ATENEA state root")
	args = flagsBeforeID(args, map[string]bool{"--state": true})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("codex certify cancel requires CERTIFICATE_ID")
	}
	store := codexcert.Store{Root: *stateRoot}
	cert, err := store.Load(flags.Arg(0))
	if err != nil {
		return err
	}
	cleanupErr := removeAndVerify(filepath.Join(*stateRoot, "codex-certification", "sandboxes", cert.ID))
	cert.CleanupVerified = cleanupErr == nil
	cert.State = codexcert.Failed
	cert.Desktop = codexcert.FailedGate(errors.New("certification canceled"))
	_ = store.DeleteChallenge(cert.ID)
	if err := store.Save(cert); err != nil {
		return err
	}
	fmt.Fprintf(out, "Certification %s canceled\n", cert.ID)
	return cleanupErr
}

func codexCertifyExport(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("codex certify export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateRoot := flags.String("state", platform.StateDir(), "ATENEA state root")
	output := flags.String("output", "certifications/codex.json", "sanitized certificate output")
	publicOutput := flags.String("public-key-output", "certifications/codex-certifier.pub", "pinned public key output")
	keyPath := flags.String("signing-key", filepath.Join(platform.StateDir(), "codex-certifier.key"), "local private signing key")
	codexPath := flags.String("codex", "codex", "Codex CLI path")
	args = flagsBeforeID(args, map[string]bool{"--state": true, "--output": true, "--public-key-output": true, "--signing-key": true, "--codex": true})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("codex certify export accepts at most one certificate id")
	}
	id := ""
	if flags.NArg() == 1 {
		id = flags.Arg(0)
	}
	store := codexcert.Store{Root: *stateRoot}
	cert, err := store.Load(id)
	if err != nil {
		return err
	}
	resolved, err := exec.LookPath(*codexPath)
	if err != nil {
		return err
	}
	current, err := codexCertificationCurrent(resolved)
	if err != nil {
		return err
	}
	if state := cert.EffectiveState(time.Now().UTC(), current); state != codexcert.Passed {
		return fmt.Errorf("codex certificate is %s", state)
	}
	privateKey, publicKey, err := codexcert.LoadOrCreateSigningKey(*keyPath)
	if err != nil {
		return err
	}
	if err := codexcert.Sign(&cert, privateKey); err != nil {
		return err
	}
	if err := codexcert.ValidateReleaseArtifact(cert, time.Now().UTC(), publicKey); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cert, "", "  ")
	if err != nil {
		return err
	}
	if err := writePinnedPublicKey(*publicOutput, publicKey); err != nil {
		return err
	}
	if err := writeArtifactAtomically(*output, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "Exported signed Codex certificate %s to %s\n", cert.ID, *output)
	return nil
}

func writePinnedPublicKey(path string, publicKey ed25519.PublicKey) error {
	encoded := codexcert.EncodePublicKey(publicKey) + "\n"
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("pinned Codex certification key must be a regular file")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		existing, decodeErr := codexcert.DecodePublicKey(bytes.TrimSpace(data))
		if decodeErr != nil {
			return decodeErr
		}
		if !bytes.Equal(existing, publicKey) {
			return errors.New("refusing to replace a different pinned Codex certification key")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err = file.WriteString(encoded); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeArtifactAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".codex-certificate-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func codexCertifyStatus(args []string, out io.Writer, require bool) error {
	flags := flag.NewFlagSet("codex certify status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateRoot := flags.String("state", platform.StateDir(), "ATENEA state root")
	jsonOut := flags.Bool("json", false, "print JSON")
	markdown := flags.Bool("markdown", false, "print Markdown")
	requireValid := flags.Bool("require-valid", require, "require a valid certificate")
	codexPath := flags.String("codex", "codex", "Codex CLI path")
	artifact := flags.String("certificate", "", "validate a sanitized certificate artifact without probing this machine")
	publicKeyPath := flags.String("public-key", "certifications/codex-certifier.pub", "trusted Ed25519 public key")
	args = flagsBeforeID(args, map[string]bool{"--state": true, "--codex": true, "--certificate": true, "--public-key": true})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("codex certify status accepts at most one certificate id")
	}
	if *jsonOut && *markdown {
		return errors.New("choose either --json or --markdown")
	}
	if *artifact != "" && flags.NArg() != 0 {
		return errors.New("certificate artifact validation does not accept a local certificate id")
	}
	id := ""
	if flags.NArg() == 1 {
		id = flags.Arg(0)
	}
	if *artifact != "" {
		data, readErr := os.ReadFile(*artifact)
		if readErr != nil {
			return readErr
		}
		var cert codexcert.Certificate
		if json.Unmarshal(data, &cert) != nil {
			return errors.New("malformed Codex certificate artifact")
		}
		keyData, keyErr := os.ReadFile(*publicKeyPath)
		if keyErr != nil {
			return keyErr
		}
		publicKey, keyErr := codexcert.DecodePublicKey(bytes.TrimSpace(keyData))
		if keyErr != nil {
			return keyErr
		}
		if err := codexcert.ValidateReleaseArtifact(cert, time.Now().UTC(), publicKey); err != nil {
			return err
		}
		fmt.Fprintf(out, "Codex certificado 100 %%\nscope=observable_app_server_cli_desktop machine=%s/%s host=%s atenea=%s codex_cli=%s codex_desktop=%s app_server_schema=%s certificate=%s commit=%s created=%s expires=%s\n", cert.Machine.OS, cert.Machine.Architecture, cert.Machine.HostnameHash, cert.Atenea.Version, cert.CodexCLI.Version, cert.CodexDesktop.Version, cert.AppServerSchemaVersion, cert.ID, cert.Commit, cert.CreatedAt.Format(time.RFC3339), cert.ExpiresAt.Format(time.RFC3339))
		return nil
	}
	store := codexcert.Store{Root: *stateRoot}
	cert, err := store.Load(id)
	if err != nil {
		return err
	}
	resolved, err := exec.LookPath(*codexPath)
	if err != nil {
		return err
	}
	current, err := codexCertificationCurrent(resolved)
	if err != nil {
		return err
	}
	state := cert.EffectiveState(time.Now().UTC(), current)
	cert.State = state
	if *jsonOut {
		data, _ := json.Marshal(cert)
		fmt.Fprintln(out, string(data))
	} else if *markdown {
		fmt.Fprintf(out, "**Codex:** %s\n\n- Alcance: contrato observable App Server, CLI y Desktop\n- Equipo: `%s/%s` · `%s`\n- Software: ATENEA `%s`; Codex CLI `%s`; Desktop `%s`; App Server `%s`\n- Certificado: `%s`\n- Creado: %s\n- Caduca: %s\n- Gates: identidad `%s`, CLI `%s`, Desktop `%s`\n", certificateLabel(state), cert.Machine.OS, cert.Machine.Architecture, cert.Machine.HostnameHash, cert.Atenea.Version, cert.CodexCLI.Version, cert.CodexDesktop.Version, cert.AppServerSchemaVersion, cert.ID, cert.CreatedAt.Format(time.RFC3339), cert.ExpiresAt.Format(time.RFC3339), cert.Identity.State, cert.CLI.State, cert.Desktop.State)
	} else {
		fmt.Fprintf(out, "Codex: %s\nscope=observable_app_server_cli_desktop machine=%s/%s host=%s atenea=%s codex_cli=%s codex_desktop=%s app_server_schema=%s certificate=%s created=%s expires=%s identity=%s cli=%s desktop=%s\n", certificateLabel(state), cert.Machine.OS, cert.Machine.Architecture, cert.Machine.HostnameHash, cert.Atenea.Version, cert.CodexCLI.Version, cert.CodexDesktop.Version, cert.AppServerSchemaVersion, cert.ID, cert.CreatedAt.Format(time.RFC3339), cert.ExpiresAt.Format(time.RFC3339), cert.Identity.State, cert.CLI.State, cert.Desktop.State)
	}
	if *requireValid && state != codexcert.Passed {
		return fmt.Errorf("codex certificate is %s", state)
	}
	return nil
}

func certificateLabel(state codexcert.State) string {
	if state == codexcert.Passed {
		return "certificado 100 %"
	}
	return string(state)
}

// flagsBeforeID permits the documented `command ID --flag value` form while
// still using the standard flag package, which otherwise stops at ID.
func flagsBeforeID(args []string, valueFlags map[string]bool) []string {
	options := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name := strings.SplitN(arg, "=", 2)[0]
		if strings.HasPrefix(arg, "-") {
			options = append(options, arg)
			lookupName := name
			if !strings.HasPrefix(name, "--") {
				lookupName = "-" + name
			}
			if valueFlags[lookupName] && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				options = append(options, args[i])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(options, positionals...)
}

func parseCertificateTTL(raw string) (time.Duration, error) {
	if strings.HasSuffix(raw, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil {
			return 0, err
		}
		if n <= 0 || n > 30 {
			return 0, errors.New("codex certificate TTL must be between one and 30 days")
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(raw)
}

func isCodexLoggedInStatus(raw string) bool {
	status := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(status, "logged in") && !strings.HasPrefix(status, "not logged in")
}

func createCertificationSandbox(root string) error {
	if _, err := os.Stat(root); err == nil {
		return errors.New("certification sandbox already exists")
	}
	for _, name := range []string{"home", "codex", "config", "state", "data", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func removeAndVerify(root string) error {
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("certification sandbox still exists after cleanup")
		}
		return fmt.Errorf("verify certification sandbox cleanup: %w", err)
	}
	return nil
}

func isolatedCodexEnvironment(root string) []string {
	keep := map[string]bool{"PATH": true, "LANG": true, "TERM": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true}
	env := []string{}
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if keep[key] {
			env = append(env, entry)
		}
	}
	env = append(env, "HOME="+filepath.Join(root, "home"), "CODEX_HOME="+filepath.Join(root, "codex"), "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_STATE_HOME="+filepath.Join(root, "state"), "XDG_DATA_HOME="+filepath.Join(root, "data"), "TMPDIR="+filepath.Join(root, "tmp"))
	return env
}

func codexCertificationCurrent(codexPath string) (codexcert.Current, error) {
	self, err := os.Executable()
	if err != nil {
		return codexcert.Current{}, err
	}
	self, _ = filepath.EvalSymlinks(self)
	atenea, err := codexcert.FileFingerprint(self, buildinfo.Full())
	if err != nil {
		return codexcert.Current{}, err
	}
	codexVersion, err := commandText(codexPath, "--version")
	if err != nil {
		return codexcert.Current{}, err
	}
	cli, err := codexcert.FileFingerprint(codexPath, codexVersion)
	if err != nil {
		return codexcert.Current{}, err
	}
	desktopPath, desktopBundle, desktopVersion, err := codexDesktopIdentity()
	if err != nil {
		return codexcert.Current{}, err
	}
	desktop, err := codexcert.FileFingerprint(desktopPath, desktopVersion)
	if err != nil {
		return codexcert.Current{}, err
	}
	schema, err := appServerSchemaDigest(codexPath)
	if err != nil {
		return codexcert.Current{}, err
	}
	profiles, _ := json.Marshal(codexcert.RequiredProfiles)
	commit, modified, fallback := buildinfo.CertificationSource()
	if strings.TrimSpace(commit) == "" {
		return codexcert.Current{}, errors.New("ATENEA binary has no observable VCS revision")
	}
	if modified {
		return codexcert.Current{}, errors.New("ATENEA binary was built from a modified working tree")
	}
	if err := verifyCertificationCheckout(commit); err != nil {
		return codexcert.Current{}, err
	}
	if fallback {
		if err := verifyCertificationRebuild(self, commit); err != nil {
			return codexcert.Current{}, err
		}
	}
	machine := codexcert.CurrentMachine()
	if machine.OS == "darwin" {
		machine.OSVersion, err = commandText("sw_vers", "-productVersion")
		if err != nil {
			return codexcert.Current{}, err
		}
	}
	atenea.Identifier = "com.tutitoos.atenea"
	cli.Identifier = "codex"
	desktop.Identifier = desktopBundle
	return codexcert.Current{Commit: commit, Atenea: atenea, CodexCLI: cli, CodexDesktop: desktop, Machine: machine, AppServerSchemaVersion: "codex-app-server/v2-experimental", AppServerSchemaSHA256: schema, ProfilesSHA256: codexcert.Hash(string(profiles)), MCPOverlaySHA256: codexcert.Hash("codex-certification-mcp/v1:" + self), PresentationSHA256: codexcert.Hash(codexcert.PresentationContract)}, nil
}

func certificationBuildArgs(commit, output string) []string {
	return []string{"build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid= -X github.com/Tutitoos/atenea/internal/buildinfo.certificationRevision=" + commit, "-o", output, "./cmd/atenea"}
}

func verifyCertificationRebuild(self, commit string) error {
	// The installed executable normally lives in ~/.local/bin, whose directory
	// is intentionally traversable. Keep the untrusted rebuild private instead
	// of rejecting that ordinary installation layout before comparison.
	rebuildDir, err := os.MkdirTemp("", "atenea-certification-rebuild-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(rebuildDir) }()
	candidate := filepath.Join(rebuildDir, "atenea")
	cmd := exec.Command("go", certificationBuildArgs(commit, candidate)...)
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rebuild certified ATENEA checkout: %w: %s", err, strings.TrimSpace(string(output)))
	}
	rebuilt, err := unsignedCertificationSHA256(candidate, rebuildDir, "rebuilt")
	if err != nil {
		return err
	}
	original, err := unsignedCertificationSHA256(self, rebuildDir, "installed")
	if err != nil {
		return err
	}
	if rebuilt != original {
		return fmt.Errorf("ATENEA binary does not match a reproducible build of commit %s", commit)
	}
	return nil
}

func unsignedCertificationSHA256(source, scratch, name string) (string, error) {
	copyPath := filepath.Join(scratch, name)
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	output, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		_ = input.Close()
		return "", err
	}
	_, copyErr := io.Copy(output, input)
	closeOutputErr := output.Close()
	closeInputErr := input.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeOutputErr != nil {
		return "", closeOutputErr
	}
	if closeInputErr != nil {
		return "", closeInputErr
	}
	if runtime.GOOS == "darwin" {
		command := exec.Command("/usr/bin/codesign", "--remove-signature", copyPath)
		if commandOutput, commandErr := command.CombinedOutput(); commandErr != nil {
			return "", fmt.Errorf("remove Mach-O signature for reproducible comparison: %w: %s", commandErr, strings.TrimSpace(string(commandOutput)))
		}
	}
	return codexcert.ReproducibleFileSHA256(copyPath)
}

func verifyCertificationCheckout(commit string) error {
	head, err := commandText("git", "rev-parse", "HEAD")
	if err != nil || head != commit {
		return errors.New("certification must run from the exact stamped ATENEA checkout")
	}
	status, err := commandText("git", "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("certification requires a clean ATENEA checkout")
	}
	return nil
}

func codexDesktopIdentity() (string, string, string, error) {
	if runtime.GOOS != "darwin" {
		return "", "", "", errors.New("codex Desktop certification requires macOS")
	}
	home, _ := os.UserHomeDir()
	for _, app := range []string{"/Applications/ChatGPT.app", "/Applications/Codex.app", filepath.Join(home, "Applications", "ChatGPT.app"), filepath.Join(home, "Applications", "Codex.app")} {
		binary := filepath.Join(app, "Contents", "MacOS", strings.TrimSuffix(filepath.Base(app), ".app"))
		if _, err := os.Stat(binary); err == nil {
			plist := filepath.Join(app, "Contents", "Info.plist")
			bundle, bundleErr := commandText("/usr/libexec/PlistBuddy", "-c", "Print :CFBundleIdentifier", plist)
			if bundleErr != nil {
				return "", "", "", bundleErr
			}
			if bundle != "com.openai.codex" {
				continue
			}
			version, versionErr := commandText("/usr/libexec/PlistBuddy", "-c", "Print :CFBundleShortVersionString", plist)
			return binary, bundle, version, versionErr
		}
	}
	return "", "", "", errors.New("codex Desktop application with bundle com.openai.codex not found")
}

func codexDesktopProcessReceipt() (string, error) {
	desktopBinary, _, _, err := codexDesktopIdentity()
	if err != nil {
		return "", err
	}
	marker := string(filepath.Separator) + "Contents" + string(filepath.Separator) + "MacOS" + string(filepath.Separator)
	cut := strings.Index(desktopBinary, marker)
	if cut < 0 {
		return "", errors.New("codex Desktop executable is outside an application bundle")
	}
	appRoot := desktopBinary[:cut]
	runner := filepath.Join(appRoot, "Contents", "Resources", "codex")
	pid := os.Getppid()
	seenDesktop, seenRunner := false, false
	ancestry := make([]string, 0, 8)
	for depth := 0; depth < 16 && pid > 1; depth++ {
		row, rowErr := commandText("ps", "-ww", "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "command=")
		if rowErr != nil {
			return "", rowErr
		}
		fields := strings.Fields(row)
		if len(fields) < 2 {
			return "", errors.New("codex Desktop process ancestry is malformed")
		}
		parent, parseErr := strconv.Atoi(fields[0])
		if parseErr != nil {
			return "", errors.New("codex Desktop parent process is malformed")
		}
		command := strings.TrimSpace(strings.TrimPrefix(row, fields[0]))
		ancestry = append(ancestry, command)
		seenDesktop = seenDesktop || command == desktopBinary || strings.HasPrefix(command, desktopBinary+" ")
		seenRunner = seenRunner || command == runner || strings.HasPrefix(command, runner+" ")
		pid = parent
	}
	if !seenDesktop || !seenRunner {
		return "", errors.New("challenge was not launched by the Codex Desktop process tree")
	}
	return codexcert.Hash(strings.Join(ancestry, "\n")), nil
}

func appServerSchemaDigest(codexPath string) (string, error) {
	root, err := os.MkdirTemp("", "atenea-codex-schema-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(root) }()
	cmd := exec.Command(codexPath, "app-server", "generate-json-schema", "--experimental", "--out", root)
	if data, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("generate App Server schema: %w: %s", err, bounded(string(data), 500))
	}
	files := []string{}
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	}); err != nil {
		return "", err
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		hash.Write([]byte(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func commandText(name string, args ...string) (string, error) {
	data, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, bounded(string(data), 500))
	}
	return strings.TrimSpace(string(data)), nil
}
func bounded(value string, limit int) string {
	if len(value) > limit {
		return value[:limit] + "…"
	}
	return value
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'" }

func inspectCodexDesktop(helper string) (string, error) {
	if helper == "" {
		for _, candidate := range []string{"/usr/local/libexec/atenea-desktop-helper", "/opt/homebrew/libexec/atenea-desktop-helper", filepath.Join("helper", ".build", "release", "atenea-desktop-helper")} {
			if info, err := os.Stat(candidate); err == nil && info.Mode()&0o111 != 0 {
				helper = candidate
				break
			}
		}
	}
	if helper == "" {
		return "", errors.New("ATENEA desktop helper not found; pass --helper")
	}
	cmd := exec.Command(helper)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	enc := json.NewEncoder(stdin)
	scan := desktopHelperScanner(stdout)
	call := func(id int, method string, params any) (map[string]any, error) {
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		if !scan.Scan() {
			if scanErr := scan.Err(); scanErr != nil {
				return nil, fmt.Errorf("read desktop helper response: %w", scanErr)
			}
			return nil, errors.New("desktop helper closed")
		}
		var response map[string]any
		if err := json.Unmarshal(scan.Bytes(), &response); err != nil {
			return nil, err
		}
		if response["error"] != nil {
			return nil, fmt.Errorf("desktop helper: %v", response["error"])
		}
		return response, nil
	}
	if _, err := call(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "atenea-codex-certification", "version": "1"}}); err != nil {
		return "", err
	}
	pid, name, err := codexDesktopPID()
	if err != nil {
		return "", err
	}
	resp, err := call(2, "tools/call", map[string]any{"name": "inspect", "arguments": map[string]any{"pid": pid, "bundle_id": "com.openai.codex", "app": name, "roles": []string{"AXStaticText", "AXTextArea", "AXGroup", "AXCheckBox"}, "budget_ms": 8000, "max_nodes": 10000, "max_bytes": 1000000, "max_depth": 100, "visual_feedback": false}})
	if err != nil {
		return "", err
	}
	text := helperContentText(resp)
	var tree map[string]any
	if json.Unmarshal([]byte(text), &tree) != nil {
		return "", errors.New("desktop helper returned malformed tree")
	}
	if tree["truncated"] != nil {
		return "", fmt.Errorf("desktop accessibility observation truncated: %v", tree["truncated"])
	}
	return flattenDesktopTree(tree), nil
}

func codexDesktopPID() (int, string, error) {
	desktopBinary, _, _, err := codexDesktopIdentity()
	if err != nil {
		return 0, "", err
	}
	data, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return 0, "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
		if command != desktopBinary && !strings.HasPrefix(command, desktopBinary+" ") {
			continue
		}
		pid, parseErr := strconv.Atoi(fields[0])
		if parseErr == nil {
			return pid, strings.TrimSuffix(filepath.Base(desktopBinary), filepath.Ext(desktopBinary)), nil
		}
	}
	return 0, "", errors.New("codex Desktop is not running")
}

func desktopHelperScanner(reader io.Reader) *bufio.Scanner {
	scan := bufio.NewScanner(reader)
	// The helper bounds raw AX text at 1 MB. JSON encoding can expand control
	// characters and then escape them again inside the JSON-RPC content string.
	scan.Buffer(make([]byte, 4096), 16<<20)
	return scan
}

func helperContentText(response map[string]any) string {
	result, _ := response["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	row, _ := content[0].(map[string]any)
	text, _ := row["text"].(string)
	return text
}
func flattenDesktopTree(tree map[string]any) string {
	nodes, _ := tree["nodes"].([]any)
	var out strings.Builder
	for _, raw := range nodes {
		row, _ := raw.(map[string]any)
		if row["bundle_id"] != "com.openai.codex" {
			continue
		}
		for _, key := range []string{"title", "value", "description"} {
			if text, ok := row[key].(string); ok {
				out.WriteString(text)
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
}
