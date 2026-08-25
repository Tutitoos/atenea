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
	// Closing the socket is the job of whoever settles the reading, not of the
	// one handler that happens to read an answer. The expiry path used to
	// resolve and leave the connection open: one socket every POLL_MS, for the
	// life of the client, against a service that had gone quiet -- which is
	// exactly when the expiry path is the one that runs.
	let connected: { end(): void } | undefined;
	let settled = false;
	let settle: (reading: Reading) => void = () => {};
	const answered = new Promise<Reading>((resolve) => {
		settle = (reading: Reading) => {
			if (settled) return;
			settled = true;
			connected?.end();
			resolve(reading);
		};
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
					connected ??= sock;
					settle(parse(buffered));
				},
				// A socket that exists, accepts, and then breaks or closes early is a
				// failure to read, not an absence: the distinction is what tells the
				// reader whether to look for a stopped service or a broken one.
				error: () => settle({ kind: "unreadable" }),
				close: () => settle({ kind: "unreadable" }),
			},
		});
		if (settled) {
			// The expiry beat the connect. Nobody is waiting for this socket
			// any more and nothing will read from it, so it is closed here
			// rather than handed to a settle that has already run.
			socket.end();
		} else {
			connected = socket;
			socket.write(REQUEST);
		}
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

type Session = { directory?: string };

type StatusApi = {
	// The client's own version, read here and never remembered: a copy of it kept
	// in this file would be a lie the moment the client upgrades underneath us,
	// and this widget now draws that line itself.
	app?: { version?: string };
	theme: { current: Record<string, string> };
	kv?: { get(key: string, fallback?: unknown): unknown };
	state?: {
		provider?: { id?: string; models?: Record<string, { cost?: { input?: number } }> }[];
		path?: { directory?: string };
		vcs?: { branch?: string };
		session?: { get(id: string): Session | undefined };
	};
	slots: {
		register(plugin: {
			order: number;
			slots: Record<string, (ctx: unknown, props: { session_id?: string }) => unknown>;
		}): string;
	};
};

type ProjectPath = { parent: string; name: string };

// The client's own path line, which this widget replaces. Rules copied from the
// component it stands in for: the session's directory when it has one, else the
// directory the client was started in; $HOME shortened to `~`; and `:branch`
// appended only when the session is the one the client is standing in.
//
// Verified against the host's render for a session whose directory differs from
// the launch cwd -- `~/Desktop/taxiprime-app/new-app`, no branch -- rather than
// against the minified helper, because three different functions in that bundle
// answer to the name the footer calls and none of them is identifiable.
//
// One quirk is not reproduced: the host joins parent and name with "/"
// unconditionally, so a session sitting exactly at $HOME renders as "/~". Here
// the separator is dropped when there is no parent.
function projectPath(api: StatusApi, sessionID: string): ProjectPath | undefined {
	const state = api.state;
	const session = sessionID ? state?.session?.get(sessionID) : undefined;
	const launched = state?.path?.directory;
	const directory = session?.directory || launched || process.cwd();
	if (typeof directory !== "string" || directory.length === 0) return undefined;

	const home = process.env.HOME;
	const short =
		home && (directory === home || directory.startsWith(`${home}/`)) ? `~${directory.slice(home.length)}` : directory;
	const branch = session?.directory && session.directory === launched ? state?.vcs?.branch : undefined;
	const parts = (branch ? `${short}:${branch}` : short).split("/");
	return { parent: parts.slice(0, -1).join("/"), name: parts.at(-1) ?? "" };
}

// The host draws a "Getting started" card in this same footer for a machine with
// no paid provider, and that card is the slot's own children -- so winning the
// slot deletes it. Both halves of its condition are readable at registration
// time, measured, so the widget can decline the footer and stay a section in the
// column above, where it costs the host nothing.
//
// Anything unreadable counts as onboarding: declining costs one line's placement,
// while guessing wrong costs a first-run user the only prompt that tells them how
// to connect a provider.
function hostIsOnboarding(api: StatusApi): boolean {
	try {
		const providers = api.state?.provider;
		if (!Array.isArray(providers)) return true;
		const paid = providers.some(
			(provider) =>
				provider?.id !== "opencode" ||
				Object.values(provider?.models ?? {}).some((model) => model?.cost?.input !== 0),
		);
		if (paid) return false;
		return api.kv?.get("dismissed_getting_started", false) !== true;
	} catch {
		return true;
	}
}

function AteneaLine(props: { api: StatusApi }) {
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

	// One line, shaped like the client's own version line directly above it: a
	// coloured bullet, the name, then the version. The bullet is the traffic light,
	// so it keeps the colour and the name does not.
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

// The client's footer, drawn by us: its path line, its version line, and the
// Atenea line adjacent to it -- which is the whole reason this widget owns the
// slot instead of sitting in the column above.
//
// The two versions come from two different places on purpose. The client's is
// read from the host on every paint; Atenea's from its socket. Neither is cached
// and neither is inferred from the other: an unreadable one says so in its own
// slot, because a stale version is the one error a version line cannot survive.
//
// Nothing here is conditional in the element tree, for the same reason as above.
// The outer gap of 1 is the host's: one blank row between the path and the
// versions, and none between the two versions, which sit in a box of their own.
function FooterSection(props: { api: StatusApi; sessionID: string }) {
	const theme = () => props.api.theme.current;
	const where = () => projectPath(props.api, props.sessionID);

	const parent = () => {
		const path = where();
		return path && path.parent ? `${path.parent}/` : "";
	};
	const name = () => where()?.name ?? "sin lectura";

	const clientVersion = () => {
		const version = props.api.app?.version;
		return typeof version === "string" && version.trim().length > 0 ? version : "sin lectura";
	};

	return (
		<box gap={1}>
			<text>
				<span style={{ fg: theme().textMuted }}>{parent()}</span>
				<span style={{ fg: theme().text }}>{name()}</span>
			</text>
			<box>
				<text fg={theme().textMuted}>
					<span style={{ fg: theme().success }}>•</span> <b>Open</b>
					<span style={{ fg: theme().text }}>
						<b>Code</b>
					</span>{" "}
					{clientVersion()}
				</text>
				<AteneaLine api={props.api} />
			</box>
		</box>
	);
}

export default {
	id: "atenea.status",
	tui: async (api: StatusApi) => {
		// Two placements, and the client's own screen decides which one. The footer
		// is a `sidebar_footer` slot declared `mode:"single_winner"`: the lowest
		// registered order wins it outright and the loser's content is dropped, so
		// 50 beats the host's 100 and there is no arrangement in which both draw.
		// Winning means owning every line that slot had, which is why the widget
		// declines it whenever the host would be using it for onboarding.
		if (hostIsOnboarding(api)) {
			api.slots.register({
				// Last in the scrolling column: the client's own sections claim 100 to
				// 500, so 900 leaves it room to add more and still keeps this at the
				// bottom, as close to the footer as a section can sit.
				order: 900,
				slots: {
					sidebar_content: () => <AteneaLine api={api} />,
				},
			});
			return;
		}

		api.slots.register({
			order: 50,
			slots: {
				sidebar_footer: (_ctx, props) => <FooterSection api={api} sessionID={props?.session_id ?? ""} />,
			},
		});
	},
};
