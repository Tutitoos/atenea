// Package clientcompat owns the versioned, evidence-backed client matrix.
// Detection is deliberately separate from functional validation: finding a
// binary or reading a version never upgrades connected, tested, presentation
// or reconnect claims.
package clientcompat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is part of ATENEA's public orchestration contract.
const SchemaVersion = "atenea.client-compat/v1"

// State is part of ATENEA's public orchestration contract.
type State string

const (
	// Pass is part of ATENEA's public orchestration contract.
	Pass State = "pass"
	// Fail is part of ATENEA's public orchestration contract.
	Fail State = "fail"
	// Unknown is part of ATENEA's public orchestration contract.
	Unknown State = "unknown"
	// Partial is part of ATENEA's public orchestration contract.
	Partial State = "partial"
)

// Valid is part of ATENEA's public orchestration contract.
func (s State) Valid() bool {
	return s == Pass || s == Fail || s == Unknown || s == Partial
}

// Evidence is part of ATENEA's public orchestration contract.
type Evidence struct {
	ID     string `json:"id"`
	Detail string `json:"detail"`
}

// EvidenceLevel is part of ATENEA's public orchestration contract.
type EvidenceLevel string

const (
	// EvidenceFixture is part of ATENEA's public orchestration contract.
	EvidenceFixture EvidenceLevel = "fixture"
	// EvidenceReal is part of ATENEA's public orchestration contract.
	EvidenceReal EvidenceLevel = "real"
	// EvidenceManual is part of ATENEA's public orchestration contract.
	EvidenceManual EvidenceLevel = "manual"
	// EvidenceUnknown is part of ATENEA's public orchestration contract.
	EvidenceUnknown EvidenceLevel = "unknown"
)

// Valid is part of ATENEA's public orchestration contract.
func (e EvidenceLevel) Valid() bool {
	return e == EvidenceFixture || e == EvidenceReal || e == EvidenceManual || e == EvidenceUnknown
}

// Check is part of ATENEA's public orchestration contract.
type Check struct {
	State    State      `json:"state"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Entry is part of ATENEA's public orchestration contract.
type Entry struct {
	Client        string        `json:"client"`
	ClientInfo    string        `json:"client_info"`
	Version       string        `json:"version"`
	Date          string        `json:"date"`
	OS            string        `json:"os"`
	Arch          string        `json:"arch"`
	Profile       string        `json:"profile"`
	Transport     string        `json:"transport"`
	Requested     string        `json:"requested"`
	Observed      string        `json:"observed"`
	Scope         string        `json:"scope"`
	EvidenceLevel EvidenceLevel `json:"evidence_level"`
	Source        EvidenceLevel `json:"source"`

	Declared     Check `json:"declared"`
	Connected    Check `json:"connected"`
	Tested       Check `json:"tested"`
	Presentation Check `json:"presentation"`
	Reconnect    Check `json:"reconnect"`
	// ServerProbe is direct Atenea/server evidence and does not certify a
	// client's connection, rendering, or reconnection.
	ServerProbe Check `json:"server_probe"`
}

// Matrix is part of ATENEA's public orchestration contract.
type Matrix struct {
	SchemaVersion string  `json:"schema_version"`
	Entries       []Entry `json:"entries"`
}

// ClientSpec is part of ATENEA's public orchestration contract.
type ClientSpec struct {
	Client           string
	ClientInfo       string
	Profile          string
	Transport        string
	Scope            string
	Overlay          string
	OverlayMechanism string
	Injection        bool
}

var specs = []ClientSpec{
	{Client: "chatgpt", ClientInfo: "ChatGPT Desktop", Profile: "chatgpt", Transport: "stdio", Scope: "desktop", Overlay: "none", OverlayMechanism: "manual", Injection: false},
	{Client: "claude", ClientInfo: "Claude Code", Profile: "claude", Transport: "stdio", Scope: "user", Overlay: "claude", OverlayMechanism: "--mcp-config", Injection: true},
	{Client: "codex", ClientInfo: "Codex CLI", Profile: "chatgpt", Transport: "stdio", Scope: "user", Overlay: "codex", OverlayMechanism: "-c", Injection: true},
	{Client: "omp", ClientInfo: "Oh My Pi", Profile: "shared", Transport: "stdio", Scope: "user", Overlay: "none", OverlayMechanism: "manual", Injection: false},
	{Client: "opencode", ClientInfo: "OpenCode", Profile: "shared", Transport: "stdio", Scope: "user", Overlay: "opencode", OverlayMechanism: "OPENCODE_CONFIG_CONTENT", Injection: true},
}

// Specs is part of ATENEA's public orchestration contract.
func Specs() []ClientSpec { return slices.Clone(specs) }

// Spec is part of ATENEA's public orchestration contract.
func Spec(client string) (ClientSpec, bool) {
	client = strings.ToLower(strings.TrimSpace(client))
	for _, spec := range specs {
		if spec.Client == client {
			return spec, true
		}
	}
	return ClientSpec{}, false
}

// NewMatrix is part of ATENEA's public orchestration contract.
func NewMatrix(now time.Time) Matrix {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	date := now.UTC().Format("2006-01-02")
	entries := make([]Entry, 0, len(specs))
	for _, spec := range specs {
		entries = append(entries, Entry{
			Client: spec.Client, ClientInfo: spec.ClientInfo, Date: date, OS: runtime.GOOS, Arch: runtime.GOARCH,
			Profile: spec.Profile, Transport: spec.Transport, Requested: "unknown",
			Observed: "unknown", Scope: spec.Scope, EvidenceLevel: EvidenceUnknown, Source: EvidenceUnknown,
			Declared: check(Unknown, "not inspected"), Connected: check(Unknown, "not tested"),
			Tested: check(Unknown, "not tested"), Presentation: check(Unknown, "not tested"),
			Reconnect: check(Unknown, "not tested"), ServerProbe: check(Unknown, "not probed"),
		})
	}
	return Matrix{SchemaVersion: SchemaVersion, Entries: entries}
}

// Normalize is part of ATENEA's public orchestration contract.
func (m Matrix) Normalize() Matrix {
	matrixCopy := Matrix{SchemaVersion: m.SchemaVersion, Entries: slices.Clone(m.Entries)}
	if matrixCopy.SchemaVersion == "" {
		matrixCopy.SchemaVersion = SchemaVersion
	}
	for i := range matrixCopy.Entries {
		matrixCopy.Entries[i] = normalizeEntry(matrixCopy.Entries[i])
	}
	sort.Slice(matrixCopy.Entries, func(i, j int) bool { return matrixCopy.Entries[i].Client < matrixCopy.Entries[j].Client })
	return matrixCopy
}

// Entry is part of ATENEA's public orchestration contract.
func (m Matrix) Entry(client string) (Entry, bool) {
	client = strings.ToLower(strings.TrimSpace(client))
	for _, entry := range m.Entries {
		if entry.Client == client {
			return entry, true
		}
	}
	return Entry{}, false
}

// JSON is part of ATENEA's public orchestration contract.
func (m Matrix) JSON() ([]byte, error) { return json.Marshal(m.Normalize()) }

// MarshalJSON is part of ATENEA's public orchestration contract.
func (e Entry) MarshalJSON() ([]byte, error) {
	type plain Entry
	return json.Marshal(plain(normalizeEntry(e)))
}

// MarshalJSON is part of ATENEA's public orchestration contract.
func (m Matrix) MarshalJSON() ([]byte, error) {
	type plain Matrix
	return json.Marshal(plain(m.Normalize()))
}

// Markdown is part of ATENEA's public orchestration contract.
func (m Matrix) Markdown() string {
	m = m.Normalize()
	var b strings.Builder
	fmt.Fprintf(&b, "# Compatibilidad de clients\n\nMatriz `%s`.\n\n", m.SchemaVersion)
	b.WriteString("| Cliente | Versión | Fecha | OS | Arch | Evidencia | Origen | Declarado | Conectado | Probado | Presentación | Reconexión | Servidor | Perfil | Transporte | Solicitado | Observado | Ámbito |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, entry := range m.Entries {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			mdCell(entry.Client), mdCell(entry.Version), mdCell(entry.Date), mdCell(entry.OS), mdCell(entry.Arch), mdCell(string(entry.EvidenceLevel)), mdCell(string(entry.Source)), entry.Declared.State, entry.Connected.State,
			entry.Tested.State, entry.Presentation.State, entry.Reconnect.State,
			entry.ServerProbe.State, mdCell(entry.Profile), mdCell(entry.Transport), mdCell(entry.Requested), mdCell(entry.Observed), mdCell(entry.Scope))
	}
	return b.String()
}

func check(state State, detail string) Check {
	if !state.Valid() {
		state = Unknown
	}
	return Check{State: state, Evidence: []Evidence{{ID: "state", Detail: sanitize(detail)}}}
}

// EvidenceCheck is part of ATENEA's public orchestration contract.
func EvidenceCheck(state State, evidence ...Evidence) Check {
	if !state.Valid() {
		state = Unknown
	}
	out := make([]Evidence, len(evidence))
	for i, item := range evidence {
		item.ID = sanitize(item.ID)
		item.Detail = sanitize(item.Detail)
		out[i] = item
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return Check{State: state, Evidence: out}
}

func normalizeEntry(entry Entry) Entry {
	entry.Client = strings.ToLower(strings.TrimSpace(entry.Client))
	entry.ClientInfo = sanitize(entry.ClientInfo)
	entry.Version = sanitize(entry.Version)
	entry.Date = sanitize(entry.Date)
	entry.OS = sanitize(entry.OS)
	entry.Arch = sanitize(entry.Arch)
	entry.Profile = sanitize(entry.Profile)
	entry.Transport = sanitize(entry.Transport)
	entry.Requested = sanitize(entry.Requested)
	entry.Observed = sanitize(entry.Observed)
	entry.Scope = sanitize(entry.Scope)
	if !entry.EvidenceLevel.Valid() {
		entry.EvidenceLevel = EvidenceUnknown
	}
	if !entry.Source.Valid() {
		entry.Source = EvidenceUnknown
	}
	entry.Declared = normalizeCheck(entry.Declared)
	entry.Connected = normalizeCheck(entry.Connected)
	entry.Tested = normalizeCheck(entry.Tested)
	entry.Presentation = normalizeCheck(entry.Presentation)
	entry.Reconnect = normalizeCheck(entry.Reconnect)
	entry.ServerProbe = normalizeCheck(entry.ServerProbe)
	return entry
}

func normalizeCheck(check Check) Check {
	if !check.State.Valid() {
		check.State = Unknown
	}
	check.Evidence = slices.Clone(check.Evidence)
	for i := range check.Evidence {
		check.Evidence[i].ID = sanitize(check.Evidence[i].ID)
		check.Evidence[i].Detail = sanitize(check.Evidence[i].Detail)
	}
	sort.Slice(check.Evidence, func(i, j int) bool { return check.Evidence[i].ID < check.Evidence[j].ID })
	return check
}

var (
	secretPattern = regexp.MustCompile(`(?i)(bearer\s+|token|password|secret|api[_-]?key)[=: ]+[^\s,;]+`)
	pathPattern   = regexp.MustCompile(`(?:/Users/|/home/|/tmp/|[A-Za-z]:[\\/])[^\s,;]+`)
)

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = secretPattern.ReplaceAllString(value, "$1<redacted>")
	value = pathPattern.ReplaceAllString(value, "<path>")
	if len(value) > 240 {
		value = value[:240] + "..."
	}
	return value
}

func mdCell(value string) string {
	return strings.ReplaceAll(sanitize(value), "|", `\|`)
}
