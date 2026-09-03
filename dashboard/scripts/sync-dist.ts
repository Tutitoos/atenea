import { cp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(process.cwd(), "..");
const source = resolve(process.cwd(), "build/client");
const destination = resolve(root, "internal/dashboard/web/dist");

await rm(destination, { recursive: true, force: true });
await mkdir(destination, { recursive: true });
await cp(source, destination, { recursive: true });

async function normalizeGeneratedText(directory: string): Promise<void> {
	for (const entry of await readdir(directory, { withFileTypes: true })) {
		const path = resolve(directory, entry.name);
		if (entry.isDirectory()) {
			await normalizeGeneratedText(path);
			continue;
		}
		if (!/\.(?:css|html|js|json)$/.test(entry.name)) continue;
		const contents = await readFile(path, "utf8");
		await writeFile(path, contents.replace(/[ \t]+$/gm, ""));
	}
}

await normalizeGeneratedText(destination);
