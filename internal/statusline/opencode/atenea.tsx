/** @jsxImportSource @opentui/solid */

// A section in the client's sidebar reporting the Atenea service: its traffic
// light, the version it is actually running, and unread incidents.
//
// The reading comes from Atenea's own unix socket -- the same door its CLI knocks
// on -- because it answers without a handshake and needs no model in the loop.
// That socket is not a declared contract, and this file pays for that choice in
// the only honest currency there is: any reply it cannot fully understand is
// drawn as "sin lectura", never as a colour. A green light nobody computed is the
// one failure a status line can have that looks exactly like success.
//
// Element shape follows feature-plugins/home/footer.tsx: <span> is only valid
// inside <text>. A <span> parented directly by a <box> is mounted and drawn as
// nothing -- measured, and it cost a full round of "the plugin is not loading".

import { createSignal, onCleanup, onMount } from "solid-js";
import { existsSync } from "node:fs";

const SOCKET = `${process.env.XDG_STATE_HOME || `${process.env.HOME}/.local/state`}/atenea/run/core.sock`;
const REQUEST = `{"jsonrpc":"2.0","id":1,"method":"atenea/status"}\n`;

// Shorter than the poll interval on purpose, so two readings can never overlap.
const TIMEOUT_MS = 1000;
const POLL_MS = 3000;

// The service sends the light as a name; a service from before that change sends
// the iota. Both are accepted because an upgrade replaces the binary while the
// old service still holds the socket, and refusing the number would blank this
// line for that whole window. Anything else is not guessed.
const NAMES = ["green", "amber", "red"] as const;
type LightName = (typeof NAMES)[number];

type Reading =
	| { kind: "light"; light: LightName; version: string; unread: number }
	| { kind: "off" }
	| { kind: "unreadable" };

function readLight(value: unknown): LightName | undefined {
	if (typeof value === "string") return NAMES.find((name) => name === value);
	if (typeof value === "number" && Number.isInteger(value)) return NAMES[value];
}

// Reads the whole reply before parsing: the payload is over 7 KB and a stream is
// free to split it. Parsing the first chunk would work on this machine today and
// fail on a slower one later, which is the kind of bug that arrives as a colour.
function parse(payload: string): Reading {
	let reply: unknown;
	try {
		reply = JSON.parse(payload);
	} catch {
		return { kind: "unreadable" };
	}
	if (!reply || typeof reply !== "object" || !("result" in reply)) return { kind: "unreadable" };
	const status = reply.result;
	if (!status || typeof status !== "object") return { kind: "unreadable" };

	const light = "Light" in status ? readLight(status.Light) : undefined;
	const version = "Version" in status && typeof status.Version === "string" ? status.Version : undefined;
	const incidents = "Incidents" in status && status.Incidents && typeof status.Incidents === "object" ? status.Incidents : undefined;
	const unread = incidents && "Unread" in incidents && typeof incidents.Unread === "number" ? incidents.Unread : undefined;

	if (light === undefined || version === undefined || version.length === 0 || unread === undefined) {
		return { kind: "unreadable" };
	}
	return { kind: "light", light, version, unread };
}

async function ask(): Promise<Reading> {
	let settle: (reading: Reading) => void = () => {};
	const answered = new Promise<Reading>((resolve) => {
		settle = resolve;
	});
	const expire = setTimeout(() => settle({ kind: "unreadable" }), TIMEOUT_MS);

	let buffered = "";
	try {
		const socket = await Bun.connect({
			unix: SOCKET,
			socket: {
				data(sock, chunk: Buffer) {
					buffered += chunk.toString();
					if (!buffered.includes("\n")) return;
					settle(parse(buffered));
					sock.end();
				},
				// A socket that exists, accepts, and then breaks or closes early is a
				// failure to read, not an absence: the distinction is what tells the
				// reader whether to look for a stopped service or a broken one.
				error: () => settle({ kind: "unreadable" }),
				close: () => settle({ kind: "unreadable" }),
			},
		});
		socket.write(REQUEST);
	} catch {
		// Bun reports ENOENT for every failed unix connect -- a missing path, a socket
		// left behind by a crash, and one we are not allowed to open all arrive as
		// errno -2 with the message "Failed to connect" (measured on Bun 1.3.14), so
		// the error itself cannot tell a stopped service from a broken one.
		// The filesystem can: no socket file is the ordinary state of a machine whose
		// service is stopped, while a socket file we could not talk to is a fault and
		// is reported as one.
		settle({ kind: existsSync(SOCKET) ? "unreadable" : "off" });
	}

	const reading = await answered;
	clearTimeout(expire);
	return reading;
}

type StatusApi = {
	theme: { current: Record<string, string> };
	slots: { register(plugin: { order: number; slots: Record<string, () => unknown> }): string };
};

function AteneaSection(props: { api: StatusApi }) {
	// Undefined until the first answer lands. Starting at "unreadable" would paint
	// an amber warning for the ~100 ms before anything had been asked, which is a
	// false alarm: not having read yet is not the same as failing to read.
	const [reading, setReading] = createSignal<Reading | undefined>();
	const theme = () => props.api.theme.current;

	onMount(() => {
		let inFlight = false;
		const poll = async () => {
			if (inFlight) return;
			inFlight = true;
			try {
				setReading(await ask());
			} finally {
				inFlight = false;
			}
		};
		void poll();
		// A bare timer is not one of the registrations the plugin runtime tracks and
		// disposes, so it is paired here by hand. Every setInterval in the host's own
		// TUI does the same.
		const timer = setInterval(() => void poll(), POLL_MS);
		onCleanup(() => clearInterval(timer));
	});

	// Every reading below is taken through an accessor called inside JSX, so each
	// one re-evaluates when the signal moves. Show's function-child form was tried
	// first and its whole subtree is dropped silently by this renderer -- measured,
	// and indistinguishable from a plugin that never loaded.
	const dot = () => {
		const now = reading();
		if (!now || now.kind === "off") return theme().textMuted;
		if (now.kind === "unreadable") return theme().warning;
		if (now.light === "green") return theme().success;
		if (now.light === "amber") return theme().warning;
		return theme().error;
	};

	// The version is printed exactly as the service reports it, build metadata and
	// all. Trimming `+751972f.modified` down to `0.10.1` would hide precisely the
	// part that says this binary is not the one that was tagged.
	//
	// The name is not repeated here: it is the section title now, so this line
	// carries only what changes.
	const words = () => {
		const now = reading();
		if (!now) return "…";
		if (now.kind === "off") return "apagado";
		if (now.kind === "unreadable") return "sin lectura";
		return now.version;
	};

	// Both halves collapse to an empty string rather than to a conditional element.
	// Wrapping this subtree in Show -- as a function child or with plain children --
	// makes the renderer drop it silently, so nothing here is conditional in the
	// element tree: only the strings change. Measured on 1.18.16.
	const glyph = () => (reading() ? "⊙ " : "");

	const unread = () => {
		const now = reading();
		return now?.kind === "light" ? now.unread : 0;
	};

	const unreadWords = () => (unread() > 0 ? `${unread()} sin leer` : "");

	// One line, the way the client writes its own version line: a coloured bullet,
	// the name, then the version. The bullet is the traffic light, so it keeps the
	// colour and the name does not.
	//
	// The unread count is a sibling in a row box rather than a line of its own. In
	// this column an empty <text> costs a visible blank row -- measured -- and an
	// empty one beside a sibling costs nothing, which is how the count disappears
	// when there is nothing unread.
	return (
		<box flexDirection="row" gap={1}>
			<text fg={theme().text}>
				<span style={{ fg: dot() }}>{glyph()}</span>
				Atenea <span style={{ fg: theme().textMuted }}>{words()}</span>
			</text>
			<text fg={theme().error}>{unreadWords()}</text>
		</box>
	);
}

export default {
	id: "atenea.status",
	tui: async (api: StatusApi) => {
		api.slots.register({
			// Last in the column. The client's own sections claim 100 to 500, so 900
			// leaves room for it to add more and still keeps this at the bottom, which
			// is where a standing service line belongs -- next to the client's own
			// version rather than above the reading for this session.
			//
			// Directly *under* the client's `OpenCode <version>` line is not reachable:
			// that line is the children of a `sidebar_footer` slot declared
			// `mode:"single_winner"`, the lowest registered order wins it outright, and
			// a winning callback is handed only `{session_id}` -- `children` is
			// undefined, measured. Sitting under that line would mean winning the slot
			// and reimplementing the client's footer, which would delete the real
			// version line the moment our copy of it drifted.
			order: 900,
			slots: {
				sidebar_content: () => <AteneaSection api={api} />,
			},
		});
	},
};
