/** @jsxImportSource @opentui/solid */

// Package-adjacent note: this file is embedded in the Atenea binary and written
// into the client's plugin directory by `atenea statusline install limits`.
//
// One collapsible section per provider: how much of each live rate-limit window
// is left, as a bar, and nothing at all when no window is live.
//
// WHERE THE NUMBERS COME FROM, AND THE MISTAKE THIS FILE USED TO MAKE. The first
// version of this widget read `~/.claude.json` for Claude and a codex session
// rollout for codex, and printed one line per window because those two files are
// refreshed only by a human: two Claude refreshes in twelve days, codex readings
// on three days out of thirty-one. Measuring the *client that rewrites a file*
// and then reporting on *the number inside it* is how that conclusion came out
// backwards -- the machine had, the whole time, a source that refreshes itself.
//
// omp keeps one usage report per provider in its own store, under a `cache` key
// of `usage_cache:report:*`, and refreshes each roughly every ten minutes while
// it runs. Measured on 2026-08-11: readings one minute old for both providers,
// median gap between readings one hour, and the report carries what a
// hand-rolled rule would otherwise have to guess -- `window.durationMs`,
// `window.id` already short as `5h` and `7d`, `amount.usedFraction`, a vendor
// `status`, and `scope.tier` marking a window that belongs to one model rather
// than to the account.
//
// Not `usage_history` in the same store, which looks like the obvious table and
// is a trap: it prunes -- 7 029 rows while its maximum advances -- and writes one,
// two or three of a provider's limits per reading, so "the newest row per
// provider" silently loses whichever window was not in the last write. A row read
// at 01:01 was gone eight minutes later. The `cache` row is the whole report,
// written at once.
//
// WHY A SECTION AND NOT A LINE. The first version was one line, and the argument
// for it was that a section whose body is empty almost always teaches the reader
// to skip that part of the screen. That argument was built on the 4%-live figure
// above, which was an artefact of the wrong source. With a reading that refreshes
// itself the body has something to say whenever the client is open, so the shape
// that costs one row closed and answers on a click is the honest one.

import { createSignal, onCleanup, onMount } from "solid-js";

// The report refreshes about every ten minutes; this poll exists to pick that up
// and to age the reading honestly, not to watch a number climb.
const POLL_MS = 60_000;

// Measured, not chosen: a `sidebar_content` <text> gets 37 columns, and the
// renderer WRAPS rather than clips -- a 38-column line becomes two rows in
// silence. Rulers of known length drawn inside the column reported 37 at 200, 130
// and 121 columns of terminal, so this is the column's own width and not a share
// of the window's.
const BUDGET = 37;

// Twenty cells leaves the widest row at 34 of the 37, which is the whole reason
// the bar is not wider: the three columns of slack absorb a `100%` and a
// three-digit reset without wrapping.
const BAR = 20;

// A reading older than this shows its age on every row it produced. The figure is
// the measured p90 gap between refreshes (65 min) plus slack: below it, an absent
// age means "current", and above it the reader is told how old the number is
// before they plan against it.
const STALE_MS = 90 * 60_000;

type Win = {
	id: string;
	usedFraction: number;
	left: number;
	resetsAt: number | null;
	durationMs: number | null;
	tier: string | null;
	ok: boolean;
};

// Three states, and the split between the last two is the point.
//
// `quiet` is a legitimate answer: the store was read, the report was understood,
// and it has nothing live to say -- either every window's reading is older than
// the window itself, or this machine does not use that provider at all. That draws
// nothing.
//
// `unreadable` is a defect: the store would not open, or it opened and this file
// did not recognise what was inside. That draws one amber line.
//
// They were the same state until 2026-08-11, and that was the worst defect in this
// widget. Every other panel here already separates them -- `Models` says
// `sin lectura` rather than showing stale shares -- and this one, alone, let a
// broken reading render as the most ordinary answer on the screen: an absent
// section, which is exactly what a quiet provider looks like. The source is another
// product's private store, reached by a fixed path with no declared schema, so the
// day it moves is a question of when.
type Reading =
	| { kind: "live"; wins: Win[]; readAt: number }
	| { kind: "quiet" }
	| { kind: "unreadable" };

function home(): string {
	return process.env.HOME ?? "";
}

// omp's own store. Read-only, and while omp is writing: the store runs in WAL, so
// a reader does not block a writer and does not see a half-written report.
function ompStore(): string {
	return `${home()}/.omp/agent/agent.db`;
}

// Six reports live here, one per provider, and the key carries a numeric prefix
// and the provider's endpoint URL. The provider is read out of the JSON instead of
// parsed off the key, because the key's shape is omp's business and the field is
// the contract. Six rows on a 3.9 MB store: the query measured 0.97 ms.
const QUERY = `SELECT value FROM cache WHERE key LIKE 'usage_cache:report:%'`;

// The client bundles its own sqlite; nothing in this checkout carries types for
// it, so the module surface this file uses is named once here -- the same single
// assertion session-share.tsx makes, covering a builtin whose shape is fixed by
// the runtime rather than the rows it returns.
type SqliteRows = { all(...params: unknown[]): unknown[] };
type SqliteHandle = { query(sql: string): SqliteRows; close(): void };
type SqliteModule = { Database: new (path: string, options: { readonly: boolean }) => SqliteHandle };

function num(value: unknown): number | null {
	return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function record(value: unknown): Record<string, unknown> {
	return value && typeof value === "object" ? (value as Record<string, unknown>) : {};
}

// Cached values are wrapped as `{ value, expiresAt }`, and the inner report
// arrives as an object here and as a string on other rows, so both are accepted.
function unwrap(raw: unknown): Record<string, unknown> {
	if (typeof raw !== "string") return {};
	let outer: unknown;
	try {
		outer = JSON.parse(raw);
	} catch {
		return {};
	}
	let inner = record(outer).value;
	if (typeof inner === "string") {
		try {
			inner = JSON.parse(inner);
		} catch {
			return {};
		}
	}
	return record(inner);
}

// THE STALENESS RULE, BOTH HALVES.
//
// The first half is a threshold: past STALE_MS the age is printed beside the
// figure, because a limit figure without one reads exactly like a fresh figure,
// and a plan made against a number that expired is a plan made against nothing.
//
// The second half is not a threshold and is the one that matters. When the
// reading is older than the window it describes, the window it described is
// OVER -- a new one opened while nobody was looking, and its usage starts from a
// number this machine never saw. Such a row is not old, it is about a different
// window, so printing it with an age attached would still be printing a wrong
// number politely. It is dropped entirely.
//
// This is the morning path, not an edge case: measured on this machine, the gaps
// between readings run to 14.5 hours overnight, and 17 of the last 30 days had a
// gap longer than five hours. Opening a laptop after one of those means the 5h
// window is always in exactly this state, while the 7d windows are merely old.
// The count of windows this file recognised but dropped for being out of their own
// window travels back with the survivors, because it is what separates a report
// that said nothing live from a report this file no longer understands.
type Scan = { wins: Win[]; aged: number; entries: number };

function windows(report: Record<string, unknown>, age: number): Scan {
	const raw = report.limits;
	if (!Array.isArray(raw)) return { wins: [], aged: 0, entries: -1 };
	let aged = 0;
	const out: Win[] = [];
	for (const entry of raw) {
		const limit = record(entry);
		const amount = record(limit.amount);
		const window = record(limit.window);
		const scope = record(limit.scope);

		// A bar needs a fraction of a known whole. `percent` is the only unit here
		// that has one; the providers reporting `requests` or `unknown` are not
		// drawn as bars rather than drawn as invented ones.
		if (amount.unit !== "percent") continue;
		const used = num(amount.usedFraction);
		const left = num(amount.remaining);
		if (used === null || left === null) continue;

		const duration = num(window.durationMs);
		if (duration !== null && age > duration) {
			aged++;
			continue;
		}

		const id = typeof window.id === "string" && window.id !== "" ? window.id : "?";
		// `scope.tier` arrives lowercase (`fable`) while the vendor's own label for
		// the same window is `Claude 7 Day (Fable)`. The first letter is raised to
		// match what the provider prints, rather than inventing a display name.
		const raw = typeof scope.tier === "string" && scope.tier !== "" ? scope.tier : null;
		const tier = raw === null ? null : raw.charAt(0).toUpperCase() + raw.slice(1);
		out.push({
			id,
			usedFraction: used,
			left,
			resetsAt: num(window.resetsAt),
			durationMs: duration,
			tier,
			ok: limit.status === "ok" || limit.status === undefined,
		});
	}
	// Fastest window first, which is also the drop order for the closed header:
	// the 5h one can stop you within the hour, the 7d one moves slowly enough to
	// be noticed in passing.
	out.sort((a, b) => (a.durationMs ?? Number.MAX_SAFE_INTEGER) - (b.durationMs ?? Number.MAX_SAFE_INTEGER));
	return { wins: out, aged, entries: raw.length };
}

function read(provider: string, now: number): Reading {
	let db: SqliteHandle | undefined;
	try {
		const sqlite = require("bun:sqlite") as SqliteModule;
		db = new sqlite.Database(ompStore(), { readonly: true });
		let best: Record<string, unknown> | undefined;
		let bestAt = -1;
		let named = false;
		for (const row of db.query(QUERY).all()) {
			const report = unwrap(record(row).value);
			if (report.provider !== provider) continue;
			named = true;
			const at = num(report.fetchedAt);
			if (at === null || at <= bestAt) continue;
			best = report;
			bestAt = at;
		}
		// No report for this provider is not a failure: a machine that does not use
		// codex has no codex report, and saying `sin lectura` there would be a
		// permanent complaint about an absence nobody asked about.
		//
		// A report that names the provider and carries no readable `fetchedAt` is the
		// opposite: the row is there and this file cannot date it, which is the shape
		// changing. Without a date there is no age, and an undated limit figure is
		// the one thing this widget refuses to print.
		if (!best) return named ? { kind: "unreadable" } : { kind: "quiet" };

		const scan = windows(best, now - bestAt);
		if (scan.wins.length > 0) return { kind: "live", wins: scan.wins, readAt: bestAt };

		// Understood and quiet: every window this file recognised was older than
		// itself, or the provider published an empty list.
		if (scan.aged > 0 || scan.entries === 0) return { kind: "quiet" };

		// A report with entries in it and not one this file could read. That is the
		// shape changing under us, and it is the case this state exists for.
		return { kind: "unreadable" };
	} catch {
		// The store would not open or would not answer. Unlike a missing report,
		// this is a defect worth a row: a fixed path into another product's private
		// store is exactly the thing that moves without telling anybody.
		return { kind: "unreadable" };
	} finally {
		try {
			db?.close();
		} catch {
			// Closing is best effort: a leaked read handle on a WAL database blocks
			// nobody, and throwing here would turn a good reading into silence.
		}
	}
}

// Rounded down, and in the unit that keeps the figure short: `hace 14h` carries
// the shape of the staleness and `hace 840m` does not read faster for being more
// precise.
function age(ms: number): string {
	const minutes = Math.max(0, Math.floor(ms / 60_000));
	if (minutes < 60) return `hace ${minutes}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `hace ${hours}h`;
	return `hace ${Math.floor(hours / 24)}d`;
}

// Coarse on purpose: the reader is deciding whether to wait, and `3d0h` answers
// that while `3d 0h 14m` spends three columns on a minute nobody waits for.
function until(ms: number): string {
	const minutes = Math.floor(ms / 60_000);
	const hours = Math.floor(minutes / 60);
	if (hours < 1) return `${minutes}m`;
	if (hours < 24) return `${hours}h${minutes % 60}m`;
	return `${Math.floor(hours / 24)}d${hours % 24}h`;
}

// Filled cells are what has been spent, so the bar empties as the window fills --
// the same direction as the number beside it, which reads `free`. A percentage
// with no visible base is the defect this whole widget was built against, so the
// base is in the word.
function bar(usedFraction: number): string {
	const full = Math.max(0, Math.min(BAR, Math.round(usedFraction * BAR)));
	return "\u2588".repeat(full) + "\u2591".repeat(BAR - full);
}

function row(win: Win): string {
	return `${win.id.padEnd(2)}  ${bar(win.usedFraction)} ${String(Math.round(win.left)).padStart(3)}% free`;
}

// The second line of a window: when it comes back, or -- for a window that belongs
// to one model rather than to the account -- whose window it is. The age rides
// here rather than on the bar row, which is already 34 of 37 columns.
function detail(win: Win, now: number, stale: string): string {
	const parts: string[] = [];
	if (win.tier) parts.push(`${win.tier} only`);
	else if (win.resetsAt !== null && win.resetsAt > now) parts.push(`resets in ${until(win.resetsAt - now)}`);
	if (stale) parts.push(stale);
	return parts.length === 0 ? "" : `    ${parts.join(" \u00B7 ")}`;
}

function body(reading: Reading, now: number, stale: string): string {
	if (reading.kind !== "live") return "";
	const lines: string[] = [];
	for (const win of reading.wins) {
		lines.push(row(win));
		const second = detail(win, now, stale);
		if (second) lines.push(second);
	}
	return lines.join("\n");
}

// What the closed header carries: every account-wide window, fastest first.
//
// Not just the tightest one. The tightest is usually the weekly figure, and the
// question a closed section has to answer is "is something about to stop me",
// which the five-hour window answers and the weekly one does not -- hiding the
// fast number behind a click gets that backwards. Both fit: measured at 30 of the
// 37 columns.
//
// Windows are dropped from the right until they fit, so the fastest is the one
// that survives a narrow line. A tier-scoped window never appears here: it
// describes one model, not the account, and two rows reading `7d` in one header
// invite the reader to reconcile them. It is one click away in the body.
function headline(name: string, reading: Reading | undefined, stale: string): string {
	if (!reading) return " \u2026";
	if (reading.kind !== "live") return "";
	const shown = reading.wins.filter((w) => !w.tier).map((w) => `${w.id} ${Math.round(w.left)}%`);
	if (stale) shown.push(stale);
	while (shown.length > 0) {
		const text = ` (${shown.join(" \u00B7 ")})`;
		// Two for the glyph and its space, and the name is the section's own.
		if (2 + name.length + text.length <= BUDGET) return text;
		// The age is dropped last: a figure whose age is unknown is the one thing
		// this widget refuses to print, so the window count gives way first.
		if (shown.length > 1 && stale && shown[shown.length - 1] === stale) shown.splice(shown.length - 2, 1);
		else shown.pop();
	}
	return "";
}

type SlotContext = { theme: { current: Record<string, string> } };

function LimitsSection(props: { ctx: SlotContext; name: string; provider: string; initial: Reading }) {
	const [reading, setReading] = createSignal<Reading>(props.initial);
	const [now, setNow] = createSignal(Date.now());
	const [open, setOpen] = createSignal(false);
	const theme = () => props.ctx.theme.current;

	onMount(() => {
		const poll = () => {
			const at = Date.now();
			setNow(at);
			setReading(read(props.provider, at));
		};
		// Paired by hand: a bare timer is not one of the registrations this runtime
		// disposes on unmount.
		const timer = setInterval(poll, POLL_MS);
		onCleanup(() => clearInterval(timer));
	});

	const current = () => reading();
	const stale = () => {
		const r = current();
		if (r.kind !== "live") return "";
		const gap = now() - r.readAt;
		return gap > STALE_MS ? age(gap) : "";
	};

	// Amber for what the vendor itself flags, for a reading old enough to carry its
	// age, and for a reading this file could not make sense of. Nothing here invents
	// a threshold on the percentage: the provider publishes a `status` per window and
	// it is the one figure in this file nobody on this side had to guess.
	const colour = () => {
		const r = current();
		if (r.kind === "unreadable") return theme().warning;
		const loud = r.kind === "live" && (stale() !== "" || r.wins.some((w) => !w.ok));
		return loud ? theme().warning : theme().textMuted;
	};

	// One element tree, and only strings inside it change. That is not style: this
	// renderer drops a `<Show>` subtree in silence and builds a mapped array once,
	// so a component that swapped its own shape per state would draw the first state
	// it ever had, forever.
	//
	// So the three states are three sets of strings:
	//
	//   live        `▶ Claude (5h 78% · 7d 56%)`, or the bars when open
	//   unreadable  `Claude sin lectura`, amber, no arrow -- there is nothing to open
	//   quiet       every string empty, which draws a blank row
	//
	// That last row is the one compromise here, and it is only reachable by going
	// quiet AFTER having been live -- five hours of a client left open while omp
	// never runs. A section that has already drawn cannot un-draw itself: the node
	// exists. The choice is a blank row or a label saying nothing, and a blank row
	// is at least not read as news. At startup the same state costs nothing at all,
	// because the slot returns null before any node exists.
	const glyph = () => (current().kind === "live" ? (open() ? "\u25BC " : "\u25B6 ") : "");
	const label = () => (current().kind === "quiet" ? "" : props.name);
	const tail = () => {
		const r = current();
		if (r.kind === "unreadable") return " sin lectura";
		if (r.kind !== "live" || open()) return "";
		return headline(props.name, r, stale());
	};

	// Mouse only, which is parity with the client's own MCP section rather than a
	// shortfall: it collapses with `onMouseDown` and a local signal, with no command
	// and no key binding anywhere near it. Collapsed is the state this section wants
	// on every launch, so the open state is deliberately not persisted.
	//
	// Header and body share one <text>: an empty <text> costs a visible blank row,
	// so a separate body node would leave a gap in the column whenever this is shut.
	return (
		<box
			onMouseDown={() => {
				if (current().kind === "live") setOpen((v) => !v);
			}}
		>
			<text fg={colour()}>
				<span style={{ fg: theme().text }}>
					{glyph()}
					<b>{label()}</b>
				</span>
				<span style={{ fg: colour() }}>{tail()}</span>
				{open() ? `\n${body(current(), now(), stale())}` : ""}
			</text>
		</box>
	);
}

type Api = {
	theme: { current: Record<string, string> };
	slots: { register(plugin: { order: number; slots: Record<string, unknown> }): string };
};

// The slot callback for one provider.
//
// The reading happens here, before anything is returned, because this callback is
// invoked exactly once -- measured, with a counter that stayed at 1 across
// repaints. A `quiet` reading draws nothing at all: no header, no placeholder, not
// even the separator the host puts between sections.
//
// An `unreadable` one draws, and that asymmetry is the whole point of the split. A
// provider that has nothing live to say and a store this file can no longer read
// are the same absence on screen, and one of them is a defect. Once the node
// exists it keeps polling, so the day the store comes back the line turns into the
// section by itself -- measured: a store rewritten under a running client was
// picked up on the next poll, without a restart.
//
// The cost of the single invocation is stated rather than hidden: a provider that
// is quiet when the client starts stays absent until the client is restarted, even
// if a reading arrives ten minutes later. It takes a reading older than the longest
// window -- eight days away from the machine -- to land there, and the alternative
// is a row that exists to say nothing.
function mount(api: Api, name: string, provider: string) {
	return () => {
		const initial = read(provider, Date.now());
		if (initial.kind === "quiet") return null;
		return <LimitsSection ctx={api} name={name} provider={provider} initial={initial} />;
	};
}

// Two providers, and only the two this machine pays for. Four others publish live
// readings into the same store -- google-antigravity, github-copilot, xai-oauth,
// minimax-code -- and none is drawn: two report in units with no whole to divide by
// (`requests`, `unknown`), one has said 100% free on all three of its windows every
// time it has been measured, and one had not been read for two days. Adding a
// provider here is a decision about what is worth a row, not a capability.
//
// Both registrations are written out with their order as a literal, rather than
// looped over a table, because the order is the one thing about a widget that this
// repository has already lost twice and now pins in a test that reads the shipped
// file.
export default {
	id: "opencode.limits",
	tui: async (api: Api) => {
		api.slots.register({
			// Under the model share at 150, which answers the same question for the
			// session in front of you rather than the account behind it. The client's
			// own sections claim round numbers -- Context 100, MCP 200, LSP 300 -- so
			// these land between the first two.
			order: 151,
			slots: { sidebar_content: mount(api, "Claude", "anthropic") },
		});
		api.slots.register({
			// Directly under Claude. Both sections are the same shape, so the column
			// reads as one answer in two parts, and the account that stops work first
			// is the one nearer the question above it.
			order: 152,
			slots: { sidebar_content: mount(api, "Codex", "openai-codex") },
		});
	},
};
