import { describe, expect, mock, test } from "bun:test";

// Registration needs neither a renderer nor an Atenea service.
mock.module("solid-js", () => ({ createSignal() {}, onCleanup() {}, onMount() {} }));
const jsx = (type: unknown, props: unknown) => ({ type, props });
mock.module("@opentui/solid/jsx-runtime", () => ({ jsx, jsxs: jsx, jsxDEV: jsx }));
mock.module("@opentui/solid/jsx-dev-runtime", () => ({ jsx, jsxs: jsx, jsxDEV: jsx }));
const plugin = (await import("./atenea.tsx")).default;

describe("host footer ownership", () => {
	test("legacy registration leaves the host footer and onboarding untouched", async () => {
		const claims: any[] = [];
		const theme = { text: "base", textMuted: "muted", success: "green", warning: "amber", error: "red" };
		await plugin.tui({
			theme: { current: theme },
			slots: { register: (claim) => { claims.push(claim); return "test"; } },
		});
		expect(claims).toHaveLength(1);
		expect(claims[0].order).toBe(900);
		expect(Object.keys(claims[0].slots)).toEqual(["sidebar_content"]);
		expect(claims[0].slots.sidebar_content().props.theme()).toBe(theme);
	});

	test("modern registration appends and reads live theme colors", () => {
		const claims: any[] = [];
		const text = {
			base: "base", muted: "muted",
			feedback: { success: { base: "green" }, warning: { base: "amber" }, error: { base: "red" } },
		};
		plugin.setup({ theme: { text }, ui: { slot: (claim) => { claims.push(claim); return () => {}; } } } as any);
		expect(claims).toHaveLength(1);
		expect(Object.keys(claims[0]).sort()).toEqual(["append", "render"]);
		expect(claims[0].append).toBe("sidebar.footer");
		const line = claims[0].render();
		expect(line.props.theme()).toEqual({ text: "base", textMuted: "muted", success: "green", warning: "amber", error: "red" });
		text.base = "changed";
		expect(line.props.theme().text).toBe("changed");
	});
});
