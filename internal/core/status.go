package core

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/Tutitoos/atenea/internal/metrics"
	"github.com/Tutitoos/atenea/internal/notebook"
	"github.com/Tutitoos/atenea/internal/selector"
	"github.com/Tutitoos/atenea/internal/supervisor"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// Light is the traffic light used at both heights of the status screen: one for
// Atenea as a whole, one per implementation. Look at the big one, and pull the
// thread down to the tool only when it is not green.
type Light uint8

// The colors a light can show, ordered from best to worst.
const (
	LightGreen Light = iota
	LightAmber
	LightRed
)

// lightNames pairs every light with the word it travels as, and is the single
// table the screen, the encoder and the decoder all read.
//
// The wire carries the word rather than the number on purpose. Light is an
// iota, so a number pins the meaning to declaration order: inserting a state
// between two existing ones would silently reinterpret every reading a consumer
// had already learned to map. A name cannot be reordered.
//
// This matters more than it looks because a consumer outside this repository
// reads this field, and it is the one field where a wrong answer is
// indistinguishable from a right one -- green, amber and red are all plausible
// on a screen. Every other field either arrives or is obviously missing.
var lightNames = [...]string{LightGreen: "green", LightAmber: "amber", LightRed: "red"}

// String keeps answering "red" for a value outside the table: on a screen the
// worst color is the safe answer, and a status line is not the place to refuse
// to draw. The wire makes the opposite choice -- see MarshalJSON.
func (l Light) String() string {
	if int(l) < len(lightNames) {
		return lightNames[l]
	}
	return "red"
}

// MarshalJSON writes the name, and refuses a light that has none.
//
// Loud on purpose, and deliberately unlike String: shipping "red" for a value
// nobody computed would put a color on someone else's screen that no health
// check produced. Whoever adds a fourth light gets this error out of the first
// test that encodes a status, which is where it is cheap.
func (l Light) MarshalJSON() ([]byte, error) {
	if int(l) >= len(lightNames) {
		return nil, fmt.Errorf("light %d has no name: add it to lightNames", uint8(l))
	}
	return json.Marshal(lightNames[l])
}

// UnmarshalJSON reads the name, and also still reads the number.
//
// The number is not legacy tolerance for its own sake: one machine runs one
// binary, but an upgrade replaces it while a service from the previous version
// is still holding the socket, so a command and the service it asks can
// disagree about this encoding for as long as nobody restarts. A strict reader
// would answer "no service" for that whole window -- Asked returns false on any
// decode error -- and the screen would quietly drop to the command's own view.
// Reading both costs four lines.
//
// An unrecognized name or an out-of-range number is still an error. A light
// that cannot be understood is not green.
func (l *Light) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		for light, candidate := range lightNames {
			if candidate == name {
				*l = Light(light)
				return nil
			}
		}
		return fmt.Errorf("unknown light %q", name)
	}
	var number uint8
	if err := json.Unmarshal(data, &number); err != nil {
		return fmt.Errorf("light must be a name or a number: %w", err)
	}
	if int(number) >= len(lightNames) {
		return fmt.Errorf("light %d has no name", number)
	}
	*l = Light(number)
	return nil
}

// Status is the short, fixed screen: overall light, every implementation with
// its color, and where the settings came from.
type Status struct {
	Version  string
	Contract contract.Version
	Settings string
	// Role is what this process maintains. Every screen below it is honest only
	// about the process printing it: a command has no clock of its own, so the
	// rhythms it lists are the ones it would keep if it were the service, read
	// from disk. Naming the role is what keeps the reader from taking a
	// command's view for the service's.
	Role string
	// Missing names implementations the shipped catalog declares and this
	// machine's settings file does not. Settings replace the catalog rather
	// than patching it, so an older file silently misses whatever later
	// releases added -- and the first symptom is a funnel with no fallback,
	// which reads as bad luck rather than as a stale file.
	Missing      []string
	Uptime       string
	Stopping     bool
	Light        Light
	Funnel       string
	Orchestrator OrchestratorStatus
	Capabilities []CapabilityStatus
	Repositories []RepositoryStatus
	// Chats are the sessions open right now. Two clients at once is the whole
	// point of the isolation, and an isolation nobody can see is a claim.
	Chats []ChatStatus
	// Incidents is what the crash notebook has that nobody has looked at. The
	// design asks the short screen for four things and this is the fourth, so
	// it sits beside the light rather than inside it: a fault Atenea already
	// survived is not the same claim as a provider being down now.
	Incidents IncidentStatus
	// Maintenance is the background lane: the rhythms that keep the history in
	// shape. It is on the screen because these jobs have nobody waiting on
	// their return value -- that is what a beat is -- so a flush failing every
	// thirty seconds for an hour looks exactly like a flush succeeding every
	// thirty seconds for an hour to anyone not reading the notebook.
	Maintenance []MaintenanceStatus
	// Processes is every MCP server Atenea itself launched and is watching,
	// as this process's own Supervisor sees it right now. Empty when the
	// settings file manages nothing, which is not a degraded state.
	Processes []ProcessStatus
	// Servers is every [[mcp_server]] the settings file declares, with what
	// the last exchange left behind. Every declared server is here on every
	// screen, including ones nothing has touched: a server missing from this
	// list is the exact failure this field exists to end, so absence is not
	// an available state -- "unknown" is.
	Servers []ServerStatus
	// Backups is what protects the history, and whether it is actually
	// happening. A copy nobody can see the state of is a copy nobody trusts.
	Backups BackupStatus
	// Recovered is what the last ugly close cost. Empty after a clean stop,
	// which is the normal case and prints nothing.
	Recovered Recovery
}

// MaintenanceStatus is one background lane and what it has been doing.
type MaintenanceStatus struct {
	Name string
	// Every is the rhythm the lane keeps. Without it "last ran at 21:20" is
	// not a fact anybody can act on.
	Every   time.Duration
	Runs    int
	LastRun time.Time
	// Failure is what went wrong last time, empty when the lane is clean. The
	// text, not a boolean: "database is locked" and "no space left on device"
	// send whoever is reading to two different places.
	Failure string
}

// ProcessStatus is one MCP server Atenea itself launched and is watching,
// for the status screen.
//
// This is what the Supervisor this process built knows right now, not a
// fact backed by disk the way Maintenance and Backups are: there is no PID
// file, on purpose (see internal/supervisor). A short-lived command like
// `atenea status` reports its own view honestly -- on_demand shows stopped
// until something in the same process asks for it, and persistent shows
// stopped until Run has warmed it up. That view is exactly right for a
// long-running `atenea run`; for a one-shot command it says what it saw,
// which is nothing yet, and says so rather than guessing at a service it
// cannot see.
type ProcessStatus struct {
	ID    string
	Light Light
	// State is the supervisor's own word for where the process is:
	// stopped, starting, ready, restarting or down.
	State    string
	Endpoint string
	PID      int
	Port     int
	Started  time.Time
	Restarts int
	// LastReason is what the most recent failed attempt said, empty when
	// the server has never failed to start or stay up.
	LastReason string
}

// ServerStatus is one declared [[mcp_server]] and the last thing known about
// it, for the status screen.
//
// It is built from remembered state and never probes. Probing on every status
// call would pay one spawn per stdio server per screen and would reintroduce
// exactly the cost core.New refuses to pay at startup; `atenea detect` is the
// command that exists for the operator who wants the question asked now.
//
// Two different memories feed State, because two different paths can lose a
// server and only one of them was ever visible. A raw backend answers this
// process directly, so its reading comes from the exchange that just happened.
// A server behind a capability is never asked for its tools at all -- it is
// reached through an implementation -- so its state is read from the health
// the catalog already remembers for the implementations whose provider is
// this id. A server that neither path has touched is "unknown", and that is
// an answer rather than a gap.
type ServerStatus struct {
	ID string
	// Transport and Where are the prober's own two sentences about how this
	// server would be reached and at what address, so a screen and a probe
	// cannot describe one server differently.
	Transport string
	Where     string
	// Expose separates a pointer from a passthrough, and is on the screen
	// because it is what decides which of the two memories above can ever
	// say anything about this row.
	Expose string
	State  BackendState
	// Reason is the cause in the words the process itself used -- env: 'node':
	// No such file or directory, not a generic "failed". When the reading came
	// from the capability path it is prefixed with the implementation that
	// reported it, because "which of my five implementations said this" is the
	// next question a reader has.
	Reason string
	// LastChecked is when the reading was taken, and is the zero time when the
	// state is unknown. A timestamp on a state nobody established would be a
	// date attached to nothing.
	LastChecked time.Time
}

// BackupStatus is the history's insurance in one line.
//
// Latest is the load-bearing field. Count says five copies exist; only Latest
// says whether the newest of them is from this morning or from March.
type BackupStatus struct {
	Enabled bool
	Dir     string
	Every   string
	Keep    int
	Count   int
	Latest  time.Time
	// Stale is the verdict, not the arithmetic. The operator reading this
	// screen should not have to subtract Latest from now and compare it
	// against Every to learn that copying quietly stopped -- that is the
	// one fault here which produces no error anywhere, so it has to be
	// stated rather than left to be worked out.
	Stale bool
	// Failure is why the copies could not even be counted, when that happened.
	Failure string
}

// IncidentStatus is the crash notebook in one line.
//
// Unread is the number, and Latest is when the newest of them happened, which
// is what tells "three from the upgrade last month" apart from "three in the
// last minute". Unreadable counts lines the notebook could not parse; it is
// reported rather than swallowed because a torn entry is itself an incident
// nobody filed.
type IncidentStatus struct {
	Unread     int
	Unreadable int
	Latest     time.Time
	// Path is where to go and look. A count with no address is a nag.
	Path string
}

// CapabilityStatus is one capability and the providers behind it.
type CapabilityStatus struct {
	ID              string
	Summary         string
	Effects         []string
	Implementations []ImplementationStatus
}

// ImplementationStatus is one provider and its color.
type ImplementationStatus struct {
	ID       string
	Provider string
	Light    Light
	Health   contract.Health
}

// RepositoryStatus is one unit of work.
type RepositoryStatus struct {
	ID        string
	Path      string
	Scale     string
	VCS       string
	Languages []string
	Indexes   []string
}

// ChatStatus is one open session: who it belongs to, what it may authorize
// and what it is entitled to read.
type ChatStatus struct {
	ID     string
	Client string
	Uptime string
	Runs   int64
	// Grant is what this chat may authorize beyond reading, by effect name.
	// Empty means it can look and nothing else.
	Grant []string
	// Context lists the heights it may read.
	Context []string
}

// OrchestratorStatus is the agent that does the work, at a glance: who it is,
// what it may ask for, which context levels it is entitled to, and who is
// actually behind it. An agent with nobody behind it can plan and choose but
// cannot dispatch, and that has to be visible rather than inferred.
type OrchestratorStatus struct {
	Agent        string
	Type         string
	Capabilities []string
	Context      []string
	// Runners lists the ids of the far sides attached, empty when none is.
	Runners []string
	// Surfaces names the executable surface selected by runners that expose
	// one, for example codex=terminal:/opt/homebrew/bin/codex.
	Surfaces []string
	// Serves lists the implementations the attached runners can execute
	// between them.
	Serves []string
	// Unreachable lists registered implementations no attached runner can
	// execute. They survive the funnel and then fail on dispatch, so naming
	// them here is the difference between a puzzle and a fact.
	Unreachable []string
	MaxParallel int
	// Standing is what every commission from this console is granted without
	// asking, by effect name. Reading is free and not listed.
	Standing []string
	// ClientFloor is the same for a chat opened by a connected client, and
	// ClientFloorInherited says the settings file never named one -- in which
	// case the two lists are equal because one is a copy, and widening
	// Standing widens clients with it. That is the whole reason this is on
	// the screen: two equal lists with no note would look like a decision.
	ClientFloor          []string
	ClientFloorInherited bool
	Checkpoints          string
	Light                Light
}

// funnelLine says out loud which filters are wired and, crucially, how far the
// last one is to be trusted right now.
//
// It was a constant. A constant that says "estimated until an implementation
// has been measured" reads as a report and is not one: it printed the same
// sentence on a machine with an empty base and on a machine running entirely
// on real figures, which is the exact confusion the sentence was written to
// prevent. The stages are fixed, so they stay in the string; the trust is a
// fact about today, so it is looked up.
func funnelLine(measured, total int, measuring bool) string {
	const stages = "constraints -> reach -> health -> cost"
	switch {
	case !measuring:
		// Not silence. With no base the estimates are not a starting position
		// that will be overtaken -- they are the permanent answer, and that is
		// a bigger claim than any of the others here.
		return stages + " (measuring is off: ranking on declared estimates for good)"
	case total == 0:
		return stages
	case measured == 0:
		return stages + " (nothing measured yet: ranking on declared estimates)"
	case measured < total:
		return fmt.Sprintf("%s (measured for %d of %d implementations, the rest on declared estimates)",
			stages, measured, total)
	default:
		return stages + " (measured)"
	}
}

// incidents reads the crash notebook for the short screen.
//
// A notebook that cannot be read is reported as one unreadable entry rather
// than as nothing. Silence here would be the worst possible answer: the whole
// artifact exists so that a fault cannot pass unmentioned, and a status screen
// saying "no incidents" because it could not open the file would be a lie told
// by the very thing meant to prevent one.
func (c *Core) incidents() IncidentStatus {
	out := IncidentStatus{Path: c.notebook.Path()}
	read, err := c.notebook.Read()
	if err != nil {
		out.Unreadable = 1
		return out
	}
	out.Unread, out.Unreadable = read.Unread, read.Unreadable
	if fresh := read.New(); len(fresh) > 0 {
		out.Latest = fresh[len(fresh)-1].At
	}
	return out
}

// Incidents is the crash notebook, whole, for whoever wants to read it out.
//
// It changes nothing, and that is the contract the command depends on: two
// people investigating the same crash must see the same file, and neither
// should discover that the other's looking is why theirs is now marked.
func (c *Core) Incidents() (notebook.Read, error) { return c.notebook.Read() }

// ClearIncidents marks everything currently in the notebook as read and
// reports how many that was. It is the only call that moves the mark, which
// is why it is a separate verb everywhere it appears.
func (c *Core) ClearIncidents() (int, error) { return c.notebook.Clear() }

// Measurements reports everything the base has recorded at or after since.
//
// It is the reading half of the pair below, and like the notebook it changes
// nothing: two people looking see the same thing, and looking is never the
// reason a number moved.
//
// Measuring switched off is not an error here. There is simply nothing to
// report, which is the true answer and a shorter one than an excuse.
func (c *Core) Measurements(since time.Time) ([]metrics.Row, error) {
	if c.measurements == nil {
		return nil, nil
	}
	return c.measurements.Summary(context.Background(), since)
}

// ClearMeasurements empties the base, or the part of it the filter names, and
// reports what went.
//
// The base is the only thing in Atenea that decides behavior and cannot be
// edited: the catalog is a settings file, health comes back on its own, but a
// baseline poisoned by an afternoon of misconfiguration used to have exactly
// one cure -- deleting the database, which threw away every honest number
// with it. This is the surgical version, and like every other destructive act
// here it is a separate word somebody has to type.
func (c *Core) ClearMeasurements(filter metrics.Filter) (metrics.Cleared, error) {
	if c.measurements == nil {
		return metrics.Cleared{}, nil
	}
	return c.measurements.Clear(context.Background(), filter)
}

// recordedHealth reads what the measurement base concluded about every
// implementation, and how many of them it can price.
//
// It swallows its errors on purpose. This feeds the health screen, and a
// health screen that refuses to draw because one of its inputs is unavailable
// is the least useful possible response to something being wrong: the
// catalog, the incidents, the lanes and the copies are all still readable and
// are exactly what somebody is looking for at that moment. An unreadable base
// costs the promotion -- providers stay at whatever the catalog declared,
// which is where they were before any of this existed -- and the funnel line
// then says nothing has been measured, which is the honest thing to say when
// the numbers cannot be reached.
func (c *Core) recordedHealth() (map[string]metrics.Verdict, int) {
	if c.measurements == nil {
		return nil, 0
	}
	ctx := context.Background()
	verdicts, err := c.measurements.Health(ctx, time.Now().UTC())
	if err != nil {
		return nil, 0
	}
	// The funnel's own threshold, so the caption and the traces below it are
	// answering the same question. A count of implementations the base has
	// merely touched would claim a trust no decision on this machine has.
	priced, err := c.measurements.Measured(ctx, selector.BreakInSamples)
	if err != nil {
		return verdicts, 0
	}
	return verdicts, len(priced)
}

// Status builds the snapshot.
func (c *Core) Status() Status {
	status := Status{
		Version:  c.Version(),
		Contract: contract.Current,
		Settings: c.settings.Source,
		Role:     c.role.String(),
		Missing:  c.settings.Missing,
		Uptime:   c.Uptime().Truncate(time.Second).String(),
		Stopping: c.Stopping(),
		Light:    LightGreen,
	}
	status.Incidents = c.incidents()
	recorded, priced := c.recordedHealth()
	// Naming the repository is worth a few characters on a workspace and is
	// noise on a machine with one: "down" and "down on api" say the same thing
	// when api is all there is.
	located := len(c.catalog.Repositories()) > 1

	total := 0
	// byProvider is the reconciled health of the worst implementation each
	// provider carries, collected here rather than recomputed later: the
	// screen must not report a server by a different rule than the one it
	// reports that server's implementations by, and the reconciliation above
	// is that rule.
	byProvider := make(map[string]providerHealth)
	for _, capability := range c.catalog.Capabilities() {
		entry := CapabilityStatus{
			ID:      capability.ID,
			Summary: capability.Summary,
		}
		for _, effect := range capability.Effects {
			entry.Effects = append(entry.Effects, effect.String())
		}
		impls, err := c.catalog.ImplementationsFor(capability.ID)
		if err != nil {
			// Unreachable: the id came from the same catalog.
			continue
		}
		usable := 0
		for _, impl := range impls {
			total++
			// Two probes reach this line and neither is the declaration: what
			// the metrics base has on disk, and what a call in this process
			// just found. Both are per repository, both say so, and both are
			// folded in the same direction -- a failure somewhere is news, and
			// a success somewhere is not a claim about anywhere else.
			if observed, where, ok := c.catalog.Observations(impl.ID); ok {
				if observed.Reason != "" {
					observed.Reason = "on " + where + ": " + observed.Reason
				}
				impl.Health = metrics.Reconcile(impl.Health, observed)
			}
			if v, ok := recorded[impl.ID]; ok {
				health := v.Health
				if located && health.Reason != "" {
					health.Reason = "on " + v.Repository + ": " + health.Reason
				}
				impl.Health = metrics.Reconcile(impl.Health, health)
			}
			light := lightFor(impl.Health.State)
			if impl.Health.Usable() {
				usable++
			}
			entry.Implementations = append(entry.Implementations, ImplementationStatus{
				ID:       impl.ID,
				Provider: impl.Provider,
				Light:    light,
				Health:   impl.Health,
			})
			if prior, seen := byProvider[impl.Provider]; !seen ||
				impl.Health.State.Rank() > prior.health.State.Rank() {
				byProvider[impl.Provider] = providerHealth{implementation: impl.ID, health: impl.Health}
			}
			status.Light = worst(status.Light, light)
		}
		// A capability nobody can answer is a red light regardless of how
		// healthy the rest of the catalog looks.
		if usable == 0 {
			status.Light = LightRed
		}
		status.Capabilities = append(status.Capabilities, entry)
	}
	status.Funnel = funnelLine(priced, total, c.measurements != nil)

	for _, repo := range c.catalog.Repositories() {
		status.Repositories = append(status.Repositories, RepositoryStatus{
			ID:        repo.ID,
			Path:      repo.Path,
			Scale:     repo.Scale.String(),
			VCS:       repo.VCS.String(),
			Languages: repo.Languages,
			Indexes:   repo.Indexes(),
		})
	}

	for _, chat := range c.Sessions() {
		entry := ChatStatus{
			ID:     chat.ID(),
			Client: chat.Client(),
			Uptime: time.Since(chat.Opened()).Truncate(time.Second).String(),
			Runs:   chat.Runs(),
		}
		for _, effect := range chat.Grant() {
			entry.Grant = append(entry.Grant, effect.String())
		}
		for _, level := range chat.Context() {
			entry.Context = append(entry.Context, level.String())
		}
		status.Chats = append(status.Chats, entry)
	}

	status.Orchestrator = c.orchestratorStatus()
	status.Light = worst(status.Light, status.Orchestrator.Light)

	status.Maintenance = c.maintenance()
	for _, lane := range status.Maintenance {
		// A lane that failed its last pass is Atenea being unwell, not a
		// provider being down: amber, never red. The work still gets done --
		// the rows are still in the buffer and the next beat tries again --
		// so calling it red would send somebody looking for an outage that
		// is not there.
		if lane.Failure != "" {
			status.Light = worst(status.Light, LightAmber)
		}
	}

	status.Processes = c.processStatus()
	for _, p := range status.Processes {
		status.Light = worst(status.Light, p.Light)
	}
	// Deliberately not folded into status.Light. A raw stdio backend is spawned
	// on demand, so "failed" here can mean nobody has started chrome-devtools
	// today, and turning the whole screen amber for that would teach an
	// operator to ignore the top line. The capability path already moves the
	// light where a failure actually costs a capability, above.
	status.Servers = c.serverStatus(byProvider)
	status.Backups = c.backups()
	status.Backups.Stale = status.Backups.stale()
	if status.Backups.Stale {
		status.Light = worst(status.Light, LightAmber)
	}
	status.Recovered = c.recovered
	if !status.Recovered.Clean() {
		// An ugly close Atenea already recovered from is amber, not red, and
		// not green either. Green would hide that yesterday's history is
		// shorter than it was; red would claim something is broken now, when
		// the repair is exactly what makes it not broken.
		status.Light = worst(status.Light, LightAmber)
	}
	// An incident nobody has read is Atenea being unwell, and unlike the two
	// checks above this one crosses processes: the notebook is on disk, so a
	// background lane that has been failing inside the service for a day is
	// seen by every command that asks. Amber, never red, for the same reason
	// as the lane above -- and it clears when somebody says they have read
	// it, which is what `atenea incidents clear` is for.
	if status.Incidents.Unread > 0 || status.Incidents.Unreadable > 0 {
		status.Light = worst(status.Light, LightAmber)
	}
	return status
}

// maintenance reports what each background lane has been doing.
func (c *Core) maintenance() []MaintenanceStatus {
	states := c.beats.States()
	out := make([]MaintenanceStatus, 0, len(states))
	for _, state := range states {
		entry := MaintenanceStatus{
			Name:    state.Name,
			Every:   state.Every,
			Runs:    state.Runs,
			LastRun: state.LastRun,
		}
		if state.LastErr != nil {
			entry.Failure = state.LastErr.Error()
		}
		out = append(out, entry)
	}
	return out
}

// backups counts what is on disk right now rather than trusting a number the
// core kept in memory. The question this answers is "is there a copy", and
// only the disk can answer it: a core that believed its own bookkeeping would
// keep reporting five copies after somebody deleted the folder.
func (c *Core) backups() BackupStatus {
	out := BackupStatus{
		Enabled: c.settings.Backup.Enabled,
		Dir:     c.settings.Backup.Dir,
		Every:   c.settings.Backup.Every.String(),
		Keep:    c.settings.Backup.Keep,
	}
	if c.copies == nil {
		return out
	}
	taken, err := c.copies.List()
	if err != nil {
		out.Failure = err.Error()
		return out
	}
	out.Count = len(taken)
	if len(taken) > 0 {
		out.Latest = taken[0].At
	}
	return out
}

// processStatus reports every MCP server Atenea itself launched, live. A
// core with nothing managed reports none, the same way maintenance and
// backups report their own kind of nothing when the settings turned them
// off.
func (c *Core) processStatus() []ProcessStatus {
	if c.processes == nil {
		return nil
	}
	live := c.processes.Status()
	out := make([]ProcessStatus, 0, len(live))
	for _, p := range live {
		out = append(out, ProcessStatus{
			ID:         p.ID,
			Light:      processLight(p.State),
			State:      p.State.String(),
			Endpoint:   p.Endpoint,
			PID:        p.PID,
			Port:       p.Port,
			Started:    p.Started,
			Restarts:   p.Restarts,
			LastReason: p.LastReason,
		})
	}
	return out
}

// stale reports a copying rhythm that has quietly stopped happening.
//
// Two periods, not one: a copy comes due at exactly one period and the beat
// that takes it can be seconds late, so one period would flap amber on a
// perfectly healthy machine. Two is the first gap that cannot be explained by
// timing. The same caution the break-in mode applies to slowness alarms --
// no alarm until the absence is unambiguous.
//
// A machine with no copy yet is not stale. A fresh install has nothing to
// protect, and shouting about it on day one is the false alarm the design
// spent a whole row avoiding.
func (b BackupStatus) stale() bool {
	if !b.Enabled || b.Latest.IsZero() {
		return false
	}
	every, err := time.ParseDuration(b.Every)
	if err != nil || every <= 0 {
		return false
	}
	return time.Since(b.Latest) > 2*every
}

// orchestratorStatus describes the agent and the far side behind it.
func (c *Core) orchestratorStatus() OrchestratorStatus {
	card := c.agent.Card()
	out := OrchestratorStatus{
		Agent:        card.ID,
		Type:         card.Type.String(),
		Capabilities: card.Capabilities,
		MaxParallel:  c.agent.MaxParallel(),
		Checkpoints:  c.checkpoints.Dir(),
		Light:        LightGreen,
	}
	orchestrator := c.settings.Orchestrator
	out.ClientFloorInherited = orchestrator.ClientEffectsInherited
	for _, effect := range orchestrator.StandingEffects {
		out.Standing = append(out.Standing, effect.String())
	}
	for _, effect := range orchestrator.ClientEffects {
		out.ClientFloor = append(out.ClientFloor, effect.String())
	}
	for _, level := range card.Context {
		out.Context = append(out.Context, level.String())
	}
	if out.Checkpoints == "" {
		out.Checkpoints = "off"
	}
	if len(c.runners) == 0 {
		// Planning and choosing still work; dispatching does not. That is not
		// broken, but it is not ready either.
		out.Light = LightAmber
		return out
	}

	for _, runner := range c.runners {
		out.Runners = append(out.Runners, runner.ID())
		if reporter, ok := runner.(contract.SurfaceReporter); ok {
			out.Surfaces = append(out.Surfaces, runner.ID()+"="+reporter.Surface())
		}
	}
	slices.Sort(out.Surfaces)
	for _, capability := range c.catalog.Capabilities() {
		impls, err := c.catalog.ImplementationsFor(capability.ID)
		if err != nil {
			continue
		}
		for _, impl := range impls {
			if c.serves(impl.ID) {
				out.Serves = append(out.Serves, impl.ID)
			} else {
				out.Unreachable = append(out.Unreachable, impl.ID)
			}
		}
	}
	slices.Sort(out.Serves)
	slices.Sort(out.Unreachable)
	if len(out.Serves) == 0 {
		// Every provider in the catalog would fail on dispatch.
		out.Light = LightRed
	} else if len(out.Unreachable) > 0 {
		out.Light = LightAmber
	}
	return out
}

// serves reports whether any attached runner can execute that implementation.
func (c *Core) serves(implementationID string) bool {
	for _, runner := range c.runners {
		if runner.Serves(implementationID) {
			return true
		}
	}
	return false
}

// lightFor colors one provider.
//
// Down is amber, not red, and that is the documented meaning of the big light:
// red is reserved for work that cannot be done. A provider the funnel dropped
// is a provider whose work went to somebody else and got finished, which is
// the system behaving exactly as designed -- the capability with nothing left
// to answer it is the red case, and it is counted separately.
//
// This was unreachable until the record started marking providers down. From a
// CLI nothing ever probed anything, so the state never arrived and the wrong
// color never showed. On a machine with one provider permanently unusable --
// a client nobody has logged into -- it would now mean a red light that is
// always on, which is the same defect as an amber nobody can clear.
func lightFor(state contract.HealthState) Light {
	if state == contract.HealthAlive {
		return LightGreen
	}
	return LightAmber
}

func worst(a, b Light) Light {
	if b > a {
		return b
	}
	return a
}

// processLight colors one managed process.
//
// Stopped is green, not amber: an on_demand server with nothing asked of it
// yet is idle by design, the same restraint BackupStatus.stale applies to a
// fresh install with no copy yet. Starting is green for the same reason --
// WarmUp deliberately does not wait, so the first status read after Run
// begins would otherwise flap amber on every clean boot. Restarting and down
// are the two states that actually say something is wrong, and they get the
// same amber a down provider gets elsewhere on this screen: red stays for a
// capability nothing can answer, not for one server behind it.
func processLight(state supervisor.State) Light {
	switch state {
	case supervisor.StateRestarting, supervisor.StateDown:
		return LightAmber
	default:
		return LightGreen
	}
}
