package core

import (
	"slices"
	"time"

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

func (l Light) String() string {
	switch l {
	case LightGreen:
		return "green"
	case LightAmber:
		return "amber"
	default:
		return "red"
	}
}

// Status is the short, fixed screen: overall light, every implementation with
// its color, and where the settings came from.
type Status struct {
	Version      string
	Contract     contract.Version
	Settings     string
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
	// Serves lists the implementations the attached runners can execute
	// between them.
	Serves []string
	// Unreachable lists registered implementations no attached runner can
	// execute. They survive the funnel and then fail on dispatch, so naming
	// them here is the difference between a puzzle and a fact.
	Unreachable []string
	MaxParallel int
	Checkpoints string
	Light       Light
}

// funnelDescription says out loud which filters are wired and how far the last
// one is to be trusted, so nobody reads an estimate as a measurement.
const funnelDescription = "constraints -> reach -> health -> cost (estimated until an implementation has been measured)"

// Status builds the snapshot.
func (c *Core) Status() Status {
	status := Status{
		Version:  c.Version(),
		Contract: contract.Current,
		Settings: c.settings.Source,
		Uptime:   c.Uptime().Truncate(time.Second).String(),
		Stopping: c.Stopping(),
		Light:    LightGreen,
		Funnel:   funnelDescription,
	}

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
			status.Light = worst(status.Light, light)
		}
		// A capability nobody can answer is a red light regardless of how
		// healthy the rest of the catalog looks.
		if usable == 0 {
			status.Light = LightRed
		}
		status.Capabilities = append(status.Capabilities, entry)
	}

	for _, repo := range c.catalog.Repositories() {
		status.Repositories = append(status.Repositories, RepositoryStatus{
			ID:        repo.ID,
			Path:      repo.Path,
			Scale:     repo.Scale.String(),
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
	return status
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
	}
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
func lightFor(state contract.HealthState) Light {
	switch state {
	case contract.HealthAlive:
		return LightGreen
	case contract.HealthDown:
		return LightRed
	default:
		return LightAmber
	}
}

func worst(a, b Light) Light {
	if b > a {
		return b
	}
	return a
}
