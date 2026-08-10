/** @jsxImportSource @opentui/solid */

// Which model did what share of the session you are looking at, as a section in
// the client's sidebar under its Context box. Nothing here reads Atenea, and
// nothing here reads the proxy: the numbers come from the client's own store,
// opened read-only.
//
// Why not currency. The store keeps a `cost` per message, and it is computed
// from a list-price table, not from a bill. On this machine most traffic reaches
// Anthropic through a local bridge on a subscription, so a dollar figure would
// be a price nobody was charged, printed in the one unit that reads as
// authoritative. Measured on 2026-08-10 in a six-model session, the two bases
// disagree by 97 points: MiniMax-M3 is 97.8% of the tokens and 0.0% of the
// list-priced cost, while glm-5.2 is 0.7% of the tokens and 38.8% of the cost.
// So the share is over tokens, and the line always prints the base it used --
// a percentage with no visible denominator is the same defect as a traffic light
// nobody computed.
//
// Why the store and not the API. `api.state.session.messages(id)` exists and is
// tempting, but it is a window: measured against a session holding 1578
// assistant messages, it returned 100. A share of the last hundred messages is
// not a share of the session, and would drift silently as the window slid. The
// session id, on the other hand, comes from the host and nowhere else.
//
// Element shape follows atenea.tsx: <span> only inside <text>, and no
// conditional elements anywhere -- this renderer drops a <Show> subtree in
// silence. Only strings change; every state collapses to text, including the
// state of not having read yet.

import { createSignal, onCleanup, onMount } from "solid-js";

const POLL_MS = 5000;

// How many models are named before the rest collapse into a count. Two fits the
// space next to a prompt; the overflow marker keeps the line honest about the
// ones it is not showing.
const NAMED = 2;

type Slice = { model: string; tokens: number };
type Reading =
	| { kind: "share"; slices: Slice[]; hidden: number; total: number }
	| { kind: "empty" }
	| { kind: "unreadable" };

function storePath(): string {
	const data = process.env.XDG_DATA_HOME || `${process.env.HOME}/.local/share`;
	return `${data}/opencode/opencode.db`;
}

// Totals per model for one session, from the client's store.
//
// `tokens.total` is preferred because it is what the client itself wrote, and
// the parts are summed only where it is absent -- older rows predate the field.
// Filtering on session_id first is what makes this affordable: the store here is
// 3.1 GB, and the client's own index on (session_id, time_created, id) turns the
// heaviest session measured -- 1578 assistant messages -- into a 2 ms read.
const QUERY = `
	SELECT json_extract(data, '$.modelID') AS model,
	       SUM(COALESCE(
	             json_extract(data, '$.tokens.total'),
	             COALESCE(json_extract(data, '$.tokens.input'), 0)
	               + COALESCE(json_extract(data, '$.tokens.output'), 0)
	               + COALESCE(json_extract(data, '$.tokens.reasoning'), 0)
	               + COALESCE(json_extract(data, '$.tokens.cache.read'), 0)
	               + COALESCE(json_extract(data, '$.tokens.cache.write'), 0)
	           )) AS tokens
	  FROM message
	 WHERE session_id = ?
	   AND json_extract(data, '$.role') = 'assistant'
	 GROUP BY model
	 HAVING tokens > 0
	 ORDER BY tokens DESC`;

// The client bundles its own sqlite; nothing in this checkout carries types for
// it, so the module surface this file uses is named once here. It is the one
// unchecked assertion in the file, and it covers a builtin whose shape is fixed
// by the runtime -- not the rows it returns, which are narrowed below.
type SqliteRows = { all(...params: unknown[]): unknown[] };
type SqliteHandle = { query(sql: string): SqliteRows; close(): void };
type SqliteModule = { Database: new (path: string, options: { readonly: boolean }) => SqliteHandle };

function read(sessionID: string): Reading {
	let handle: SqliteHandle | undefined;
	try {
		// Required at call time, not imported at module load: a plugin that throws
		// while importing is dropped by the host with no log, no toast and no entry
		// in plugin-meta.json -- indistinguishable from one that was never
		// installed. Failing here instead costs one word on the screen.
		const sqlite = require("bun:sqlite") as SqliteModule;
		handle = new sqlite.Database(storePath(), { readonly: true });
		const rows = handle.query(QUERY).all(sessionID);

		// Rows come from a store this file does not own, so each field is checked
		// rather than described. A row that does not answer is skipped: one bad
		// row must not turn a real share into "sin lectura".
		const slices: Slice[] = [];
		for (const row of rows) {
			if (!row || typeof row !== "object") continue;
			const model = "model" in row ? row.model : undefined;
			const raw = "tokens" in row ? row.tokens : undefined;
			if (typeof model !== "string" || model === "") continue;
			const tokens = typeof raw === "number" ? raw : Number(raw);
			if (!Number.isFinite(tokens) || tokens <= 0) continue;
			slices.push({ model, tokens });
		}
		if (slices.length === 0) return { kind: "empty" };

		const total = slices.reduce((sum, s) => sum + s.tokens, 0);
		return { kind: "share", slices: slices.slice(0, NAMED), hidden: Math.max(0, slices.length - NAMED), total };
	} catch {
		// A store that cannot be read is said, never drawn as a share. The failure
		// this avoids is a line that keeps showing the last good percentages while
		// the numbers behind them have stopped arriving.
		return { kind: "unreadable" };
	} finally {
		try {
			handle?.close();
		} catch {
			// Closing is best effort; a leaked read handle on a WAL database blocks
			// nobody, and throwing here would turn a good reading into "unreadable".
		}
	}
}

// Only the date suffix comes off: `claude-haiku-4-5-20251001` is 25 characters and
// the last eight are a release date the reader did not choose.
//
// Stripping a vendor prefix was tried and reverted after seeing it on the screen:
// the ids in this store carry no provider prefix -- that is a separate field --
// so the rule only ever matched a vendor that is part of the name, and turned
// `MiniMax-M3` into `M3`. Shortening a name until it stops naming the thing is
// worse than a long line.
function short(model: string): string {
	return model.replace(/-\d{8}$/, "");
}

function magnitude(tokens: number): string {
	if (tokens >= 1_000_000_000) return `${(tokens / 1_000_000_000).toFixed(1)}G`;
	if (tokens >= 1_000_000) return `${Math.round(tokens / 1_000_000)}M`;
	if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}k`;
	return `${tokens}`;
}

// Percentages are rounded for width and can therefore sum to 99 or 101. That is
// left visible rather than fudged: the alternative is inventing a point to make
// the row add up, which is a number nobody measured.
function percent(part: number, total: number): number {
	return Math.round((100 * part) / total);
}

// Every line in the column has to say something. An empty <text> costs a visible
// blank row -- measured with a probe against the real sidebar before this was
// written -- so each state names two non-empty lines rather than hiding one.
function lines(reading: Reading | undefined): [string, string] {
	if (!reading) return ["…", "leyendo el store"];
	if (reading.kind === "unreadable") return ["sin lectura", "el store no respondio"];
	if (reading.kind === "empty") return ["sin modelos todavia", "0 tokens"];
	const named = reading.slices.map((s) => `${short(s.model)} ${percent(s.tokens, reading.total)}%`);
	const rest = reading.hidden > 0 ? `+${reading.hidden} · ` : "";
	return [named.join(" · "), `${rest}${magnitude(reading.total)} tokens`];
}

type SlotContext = { theme: { current: Record<string, string> } };
type SlotProps = { session_id?: unknown };

function ShareSection(props: { ctx: SlotContext; slot: SlotProps }) {
	const [reading, setReading] = createSignal<Reading | undefined>();
	const theme = () => props.ctx.theme.current;
	const sessionID = () => (typeof props.slot.session_id === "string" ? props.slot.session_id : "");

	onMount(() => {
		const poll = () => {
			const id = sessionID();
			if (!id) return;
			setReading(read(id));
		};
		poll();
		// Paired by hand: a bare timer is not one of the registrations this runtime
		// disposes on unmount.
		const timer = setInterval(poll, POLL_MS);
		onCleanup(() => clearInterval(timer));
	});

	const colour = () => (reading()?.kind === "unreadable" ? theme().warning : theme().textMuted);

	const rows = () => lines(reading());

	// The shape is the one the section above it uses: a bold name in the body
	// colour, muted values under it. Sitting beside `Context` and reading nothing
	// like it would make this look like a second opinion about the same number.
	return (
		<box>
			<text fg={theme().text}>
				<b>Share</b>
			</text>
			<text fg={colour()}>{rows()[0]}</text>
			<text fg={theme().textMuted}>{rows()[1]}</text>
		</box>
	);
}

export default {
	id: "opencode.session-share",
	tui: async (api: { slots: { register(plugin: { order: number; slots: Record<string, unknown> }): string } }) => {
		api.slots.register({
			// The sidebar column is ordered, and the host's own sections claim round
			// numbers: Context 100, MCP 200, LSP 300, Todo 400, Files 500. 150 puts
			// this directly under the Context box, which is where the question was
			// asked, and above MCP. Read out of the shipped bundle, not guessed.
			order: 150,
			slots: {
				// Two arguments, measured: the host passes its context first and the
				// slot's own props second. The declared type puts `session_id` among
				// the props of this slot and that part holds; the arity does not
				// appear in the type at all.
				sidebar_content: (ctx: SlotContext, slot: SlotProps) => <ShareSection ctx={ctx} slot={slot ?? {}} />,
			},
		});
	},
};
