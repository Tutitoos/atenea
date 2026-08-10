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

// Measured, not chosen: a `sidebar_content` <text> gets 37 columns, and the
// renderer WRAPS rather than clips -- a 38-column line becomes two rows in
// silence. Rulers of known length drawn inside the column reported 37 at 200, 130
// and 121 columns of terminal, so this is the column's own width and not a share
// of the window's.
//
// This constant arrived after the fact, which is the defect it exists to stop
// repeating: the closed header below was shipped at 38 columns in the worst real
// case measured on this machine, and it was wrapping into a second row without
// anybody noticing -- a collapsed section that quietly costs two rows is not
// collapsed.
const BUDGET = 37;

// Every model gets a line of its own, and the list is collapsed until asked for.
//
// There used to be a cap of five here, for a real reason: this column is a
// scrollbox, so a long list does not clip -- it pushes what follows below the
// fold, where it is still rendered and nobody reads it. Measured with a 24-row
// probe: the client's own LSP body and the Atenea line went under, and came back
// when the pane got taller.
//
// Collapsing answers that better than a cap did. Closed, the section is one line
// and cannot push anything anywhere; open, the reader asked for the length they
// got. A cap made that decision for them, and had to keep a summed remainder line
// so the visible shares still reached 100 -- a line that existed only because of
// the cap.

type Slice = { model: string; tokens: number };
type Reading =
	| { kind: "share"; slices: Slice[]; total: number }
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

		// Ordered by tokens descending in SQL, and the order is load-bearing twice:
		// the list reads as a ranking, and the closed header quotes the first row as
		// the session's dominant model.
		const total = slices.reduce((sum, s) => sum + s.tokens, 0);
		return { kind: "share", slices, total };
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
// the column add up, which is a number nobody measured.
//
// A share that rounds to zero is printed as `<1%` instead. Measured on a
// seven-model session, `gemini-3.1-pro-preview` holds 0.5% -- and a line reading
// `699k (0%)` states two things that contradict each other, which is worse than
// either being imprecise.
function percent(part: number, total: number): string {
	const share = Math.round((100 * part) / total);
	if (share === 0 && part > 0) return "<1%";
	return `${share}%`;
}

// One model per line: name, tokens, share. Both numbers on a line come from the
// same sum -- the tokens printed are exactly the ones the percentage divides -- so
// the two cannot disagree. Mixing a cache-excluded token count with a
// cache-included percentage would read as consistent and be wrong by the 9 points
// those two bases differ by on this machine.
//
// The whole list is one string with newlines. A <Show> is dropped silently by this
// renderer, a mapped array is built once and never updates, and a fixed set of
// <text> slots pays a visible blank row for every model a session does not have.
// A newline in one <text> draws as a line -- measured -- and costs nothing when
// there is no next model.
//
// No combined total: the client's own Context box, directly above this one, is
// already reporting the session's tokens. Printing a second total invites the
// reader to reconcile two numbers that answer different questions.
function body(reading: Reading): string {
	return reading.slices
		.map((s) => `${short(s.model)} ${magnitude(s.tokens)} (${percent(s.tokens, reading.total)})`)
		.join("\n");
}

// What the closed header carries. A section that says only its own name when shut
// is a line spent on a label, so the state that is never worth hiding is stated
// here instead: the dominant model and its share, or the reason there is no list.
//
// A failure is deliberately louder closed than open. If a collapsed section could
// hide `sin lectura`, closing it once would turn a broken reading into an absence
// nobody notices -- which is the failure mode this whole widget was built against.
// The name is the only part of this header that can be any length, so it is the
// part that gives way. Cutting a name is a real loss -- this file already reverted
// a rule that shortened `MiniMax-M3` to `M3`, on the grounds that shortening a
// name until it stops naming the thing is worse than a long line -- but a wrapped
// header is worse than both: it is a silent second row in a section whose whole
// point is to cost one. The cut is marked, and the full name is one click away in
// the body, where the list is free to be as wide as it needs.
function clip(name: string, room: number): string {
	if (room <= 1) return "\u2026";
	return name.length <= room ? name : `${name.slice(0, room - 1)}\u2026`;
}

function headline(reading: Reading | undefined): string {
	if (!reading) return " …";
	if (reading.kind === "unreadable") return " sin lectura";
	if (reading.kind === "empty") return " sin modelos todavia";
	const top = reading.slices[0];
	const rest = reading.slices.length - 1;
	const tail = rest > 0 ? `, +${rest}` : "";
	const share = percent(top.tokens, reading.total);
	// `▶ Models` is eight columns; the rest of the frame is ` (`, one space and `)`.
	const room = BUDGET - 8 - 2 - 1 - share.length - tail.length - 1;
	return ` (${clip(short(top.model), room)} ${share}${tail})`;
}

type SlotContext = { theme: { current: Record<string, string> } };
type SlotProps = { session_id?: unknown };

function ShareSection(props: { ctx: SlotContext; slot: SlotProps }) {
	const [reading, setReading] = createSignal<Reading | undefined>();
	const [open, setOpen] = createSignal(false);
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

	// A list to open, or nothing behind the arrow. Drawn only when opening would
	// show something, which is how the client's own MCP section behaves: it wears
	// the arrow above two servers and not below, because an arrow that opens onto
	// the line already on screen is an invitation to press it for nothing.
	const foldable = () => reading()?.kind === "share";
	const glyph = () => (foldable() ? (open() ? "\u25BC " : "\u25B6 ") : "");

	// Mouse only, and that is parity rather than a shortfall: the client collapses
	// its own MCP section with `onMouseDown` and a local signal, with no command
	// and no key binding anywhere near it. Measured from the inside, by sending a
	// real SGR click at the header cell and watching the arrow turn over.
	//
	// Open state is not remembered across restarts. `api.kv` does round-trip -- it
	// was measured -- but collapsed is the default this section wants on every
	// launch, so persisting it would only ever preserve a decision the reader made
	// about one session.
	//
	// The header and the list share one <text> on purpose: an empty <text> costs a
	// visible blank row, so a separate body node would leave a gap in the column
	// whenever this is shut. The name keeps the body colour off it with a span; the
	// rows inherit the node's colour, which is how a failed reading turns the whole
	// thing amber.
	return (
		<box
			onMouseDown={() => {
				if (foldable()) setOpen((v) => !v);
			}}
		>
			<text fg={colour()}>
				<span style={{ fg: theme().text }}>
					{glyph()}
					<b>Models</b>
				</span>
				<span style={{ fg: colour() }}>{open() ? "" : headline(reading())}</span>
				{open() && reading()?.kind === "share" ? `\n${body(reading() as Reading)}` : ""}
			</text>
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
