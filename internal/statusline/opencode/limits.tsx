/** @jsxImportSource @opentui/solid */

// Package-adjacent note: this file is embedded in the Atenea binary and written
// into the client's plugin directory by `atenea statusline install limits`.
//
// One line per live rate-limit window, and nothing at all when none is live.
//
// It is a line rather than a section, and that shape was decided by measurement
// rather than taste. These figures refresh only when a human runs a command:
// Claude's come from `~/.claude.json`, which the `claude` CLI rewrites on
// `/status` or `/usage`, and codex's come from a session rollout, which only a
// real model turn appends to. Measured on this machine: two Claude refreshes in
// twelve days, with the client itself running for 24 hours after the last one
// without touching it; codex readings on three days out of thirty-one. A
// five-hour window is therefore live about 4% of the time.
//
// So a section with a header and a row per provider would say "sin ventana viva"
// almost always, which teaches the reader to skip that part of the screen -- and
// then the day the weekly figure matters, they do not see it either. The same
// reasoning the unread counter already follows by being absent at zero.
//
// Every line carries its age, always. A limit figure without one is worse than no
// figure: a plan made against a number that expired yesterday is a plan made
// against nothing, and it reads exactly like a fresh one.
//
// Whether anything renders is decided ONCE, when the client loads plugins.
// Measured: a slot callback is invoked a single time -- a counter in one stayed at
// 1 across repaints -- and neither a callback nor a component that returned null
// is ever asked again. So a null here is permanent until the client restarts,
// which is also why nothing is faked to hold a place: `null` costs zero rows, not
// even the separator the host puts between sections, while an empty <text> costs
// a blank row and an empty <box> costs the separator.

import { createSignal, onCleanup, onMount } from "solid-js";

// Quotas move in hours, and nothing in a session refreshes them anyway. This poll
// exists to age the line honestly and to notice a file somebody refreshed in
// another terminal, not to watch a number climb.
const POLL_MS = 60_000;

type Live = {
	// provider is how the reader knows whose quota this is.
	provider: string;
	// window is the bucket: `5h`, `7d`, or whatever the vendor's own name shortens
	// to. It is never invented from a percentage.
	window: string;
	percent: number;
	// readAt is when the figure was fetched, not when it was read off disk. The age
	// printed is the age of the measurement.
	readAt: number;
	// loud is the vendor's own severity, not a threshold invented here.
	loud: boolean;
};

function home(): string {
	return process.env.HOME ?? "";
}

// Claude publishes its own list of limits, and it is authoritative in a way a
// hand-rolled rule is not: `utilization.limits` carries `kind`, `percent`,
// `severity`, `resets_at` and `is_active` per bucket. Measured here: the session
// bucket at 4% with `is_active: false` because its window closed two hours after
// the reading, and the weekly one at 29% with `is_active: true`. Reading activity
// off that flag is the difference between reporting what the vendor says and
// second-guessing it from a timestamp.
//
// The older shape is the fallback: `utilization.five_hour` and
// `utilization.seven_day`, judged live by `resets_at` alone.
const KINDS: Record<string, string> = {
	session: "5h",
	weekly_all: "7d",
	weekly_scoped: "7d",
};

function claudeLive(now: number): Live[] {
	try {
		const raw = require("fs").readFileSync(`${home()}/.claude.json`, "utf8");
		const cached = JSON.parse(raw)?.cachedUsageUtilization;
		const readAt = cached?.fetchedAtMs;
		if (typeof readAt !== "number" || !Number.isFinite(readAt)) return [];
		const util = cached?.utilization;
		if (!util || typeof util !== "object") return [];

		const out: Live[] = [];
		const limits = util.limits;
		if (Array.isArray(limits)) {
			for (const limit of limits) {
				if (!limit || typeof limit !== "object") continue;
				if (limit.is_active !== true) continue;
				const percent = limit.percent;
				if (typeof percent !== "number" || !Number.isFinite(percent)) continue;
				const kind = typeof limit.kind === "string" ? limit.kind : "";
				// A scoped weekly limit is a different limit from the account's weekly
				// one, so it says which model it scopes rather than reading as a second
				// opinion about the same number.
				const model = limit.scope?.model?.display_name;
				const scope = kind === "weekly_scoped" && typeof model === "string" ? ` ${model}` : "";
				out.push({
					provider: "Claude",
					window: `${KINDS[kind] ?? kind}${scope}`,
					percent,
					readAt,
					loud: typeof limit.severity === "string" && limit.severity !== "normal",
				});
			}
			return out;
		}

		for (const [key, window] of [
			["five_hour", "5h"],
			["seven_day", "7d"],
		] as const) {
			const bucket = util[key];
			if (!bucket || typeof bucket !== "object") continue;
			const percent = bucket.utilization;
			if (typeof percent !== "number" || !Number.isFinite(percent)) continue;
			const resets = Date.parse(String(bucket.resets_at));
			if (!Number.isFinite(resets) || resets <= now) continue;
			out.push({ provider: "Claude", window, percent, readAt, loud: false });
		}
		return out;
	} catch {
		// A missing or unreadable file is not a fault worth a line: a machine with no
		// Claude CLI has no Claude quota, and that is the silent case.
		return [];
	}
}

// codex keeps no settings field for this. The figure arrives inside a session
// rollout -- `payload.rate_limits`, with `primary` and `secondary` carrying
// `used_percent`, `window_minutes` and `resets_at` as epoch seconds -- and only a
// real model turn appends one.
//
// Rollouts are scanned newest first, and only those young enough for their reading
// to still be inside a window. That bound is what keeps this cheap: 14 files here,
// one of them recent enough to open, 54 KB. A reading older than the longest window
// cannot be live, so opening the rest could only ever produce a number this line
// refuses to print.
const MAX_FILES = 8;
const MAX_AGE_MS = 8 * 24 * 60 * 60 * 1000;

function windowName(minutes: unknown): string {
	if (typeof minutes !== "number" || !Number.isFinite(minutes) || minutes <= 0) return "?";
	if (minutes % (24 * 60) === 0) return `${minutes / (24 * 60)}d`;
	if (minutes % 60 === 0) return `${minutes / 60}h`;
	return `${minutes}m`;
}

function codexLive(now: number): Live[] {
	try {
		const fs = require("fs");
		const root = `${home()}/.codex/sessions`;
		const names: string[] = fs.readdirSync(root, { recursive: true });
		const files = names
			.filter((name) => {
				const base = name.split("/").pop() ?? "";
				return base.startsWith("rollout-") && base.endsWith(".jsonl");
			})
			.map((name) => `${root}/${name}`)
			.map((path) => ({ path, at: fs.statSync(path).mtimeMs as number }))
			.filter((file) => now - file.at < MAX_AGE_MS)
			.sort((a, b) => b.at - a.at)
			.slice(0, MAX_FILES);

		for (const file of files) {
			// Last occurrence in the file, not the first: a session's newest turn is the
			// one whose numbers are current.
			const lines = fs.readFileSync(file.path, "utf8").split("\n");
			for (let i = lines.length - 1; i >= 0; i -= 1) {
				if (!lines[i].includes('"rate_limits"')) continue;
				let entry: Record<string, any>;
				try {
					entry = JSON.parse(lines[i]);
				} catch {
					continue;
				}
				const limits = entry?.payload?.rate_limits;
				if (!limits || typeof limits !== "object") continue;
				const readAt = Date.parse(String(entry.timestamp));
				const out: Live[] = [];
				for (const key of ["primary", "secondary"]) {
					const bucket = limits[key];
					if (!bucket || typeof bucket !== "object") continue;
					const percent = bucket.used_percent;
					if (typeof percent !== "number" || !Number.isFinite(percent)) continue;
					const resets = bucket.resets_at;
					if (typeof resets !== "number" || !Number.isFinite(resets)) continue;
					if (resets * 1000 <= now) continue;
					out.push({
						provider: "codex",
						window: windowName(bucket.window_minutes),
						percent,
						readAt: Number.isFinite(readAt) ? readAt : file.at,
						loud: false,
					});
				}
				// The newest reading in the newest file is the answer, even when it holds
				// nothing live: a turn that failed for depleted credits writes
				// `primary: null`, and that is the current state rather than a reason to
				// go looking for an older number that would please the reader more.
				return out;
			}
		}
		return [];
	} catch {
		return [];
	}
}

// Rounded down, and the unit is the one that keeps the number small: a reader who
// sees `hace 23h` knows the shape of the staleness, and `hace 1380m` does not read
// faster for being more precise.
function age(ms: number): string {
	if (ms < 0) return "recien";
	const minutes = Math.floor(ms / 60_000);
	if (minutes < 1) return "hace <1m";
	if (minutes < 60) return `hace ${minutes}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `hace ${hours}h`;
	return `hace ${Math.floor(hours / 24)}d`;
}

// Percentages arrive as whole numbers from Claude and as floats from codex.
// Rounding is for width; a figure that rounds to zero on a live window is printed
// as `0%` rather than hidden, because a window you have not touched is a real
// answer to how much of it is left.
function line(live: Live, now: number): string {
	return `${live.provider} ${live.window} ${Math.round(live.percent)}% · ${age(now - live.readAt)}`;
}

function read(now: number): Live[] {
	return [...claudeLive(now), ...codexLive(now)];
}

type SlotContext = { theme: { current: Record<string, string> } };

function LimitsLine(props: { ctx: SlotContext; initial: Live[] }) {
	const [live, setLive] = createSignal<Live[]>(props.initial);
	const [now, setNow] = createSignal(Date.now());
	const theme = () => props.ctx.theme.current;

	onMount(() => {
		const poll = () => {
			const at = Date.now();
			setNow(at);
			setLive(read(at));
		};
		// Paired by hand: a bare timer is not one of the registrations this runtime
		// disposes on unmount.
		const timer = setInterval(poll, POLL_MS);
		onCleanup(() => clearInterval(timer));
	});

	// One string with newlines, which is how a list of unknown length is drawn here:
	// a <Show> is dropped silently by this renderer and a mapped array is built once
	// and never updates.
	//
	// When a window closes mid-session the string goes empty and this pays one blank
	// row until the client restarts. That is deliberate: the node exists, so the
	// choice is between a row that says nothing and a row that repeats a percentage
	// whose window has closed. A stale limit is the failure this line was built to
	// avoid, and a blank row is at least not read as news.
	const body = () => live().map((l) => line(l, now())).join("\n");
	const colour = () => (live().some((l) => l.loud) ? theme().warning : theme().textMuted);

	return <text fg={colour()}>{body()}</text>;
}

export default {
	id: "opencode.limits",
	tui: async (api: {
		theme: { current: Record<string, string> };
		slots: { register(plugin: { order: number; slots: Record<string, unknown> }): string };
	}) => {
		api.slots.register({
			// Directly under the model share, which sits at 150: both answer "what has
			// this cost", one for the session in front of you and one for the account
			// behind it. The client's own sections claim round numbers -- Context 100,
			// MCP 200, LSP 300 -- so this lands between the first two.
			order: 160,
			slots: {
				sidebar_content: () => {
					// Read before returning, because this callback is invoked once. With
					// nothing live there is nothing to draw, and returning null draws
					// nothing at all -- no header, no placeholder, no separator.
					const initial = read(Date.now());
					if (initial.length === 0) return null;
					return <LimitsLine ctx={api} initial={initial} />;
				},
			},
		});
	},
};
