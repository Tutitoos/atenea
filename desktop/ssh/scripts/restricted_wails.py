#!/usr/bin/env python3
"""Build and test pinned Wails with Atenea's host-side dispatcher guard.

The upstream module is copied to a temporary Go workspace and patched only
after exact source-hash checks. The application will not compile without the
guard marker added to that verified source copy.
"""

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
WAILS = "github.com/wailsapp/wails/v2"
WEBVIEW2 = "github.com/wailsapp/go-webview2"
WEBVIEW2_CHROMIUM_HASH = "6013bea6dc614888de282a37febebdfb31984377b317b3911671b20209b8b671"
SOURCE_HASHES = {
    "internal/frontend/dispatcher/dispatcher.go": "baa6bc120411c07323e66476bb14a9872088a970449a600a765890012beb3133",
    "internal/frontend/desktop/darwin/frontend.go": "96b4e064ea8178a0ae26e65eab5c92c4200eca3f0f241c2035f88de6eaead35e",
    "internal/frontend/desktop/darwin/WailsContext.m": "ffb03f12a11f4af78b3ea2fac0ef5b2b931bc5de0b0e835ed75554fd7ad91e72",
    "internal/frontend/desktop/linux/frontend.go": "2610e740979055e636a30d05670126aad3fa6b77071c3f9af20e2fb6c47c89f5",
    "internal/frontend/desktop/linux/window.c": "76529ab2c6eb8a3b195823f934ca14adac937b300ad0420cd6e3e6e6630b5643",
    "internal/frontend/desktop/windows/frontend.go": "9598c7f21779e2332db788a9e09f3b4856d563ab5fc2c93bf7035743b9d7befa",
    "pkg/options/options.go": "9ec72bb753c04f7bf1f5d09ab973db41791028df3f13051d5fac5c19143a6d2e",
}
MARKER = "AteneaSSHBridgeGuard"
GUARD_CALL = "\tif !ateneaSSHAllowedMessage(message) {\n\t\treturn \"\", errors.New(\"atenea ssh: bridge operation denied\")\n\t}\n"


def run(*args: str, env: dict[str, str] | None = None, cwd: Path = ROOT) -> None:
    subprocess.run(args, cwd=cwd, env=env, check=True)


def prepare_workspace(tmp: Path) -> Path:
    subprocess.run(["go", "mod", "verify"], cwd=ROOT, check=True, stdout=subprocess.DEVNULL)
    version = subprocess.check_output(
        ["go", "list", "-m", "-f", "{{.Version}}", WAILS], cwd=ROOT, text=True
    ).strip()
    if version != "v2.15.0":
        raise RuntimeError(f"expected Wails v2.15.0, found {version or 'local replacement'}")
    module_dir = Path(
        subprocess.check_output(
            ["go", "list", "-m", "-f", "{{.Dir}}", WAILS], cwd=ROOT, text=True
        ).strip()
    )
    webview_version = subprocess.check_output(
        ["go", "list", "-m", "-f", "{{.Version}}", WEBVIEW2], cwd=ROOT, text=True
    ).strip()
    if webview_version != "v1.0.22":
        raise RuntimeError(f"expected go-webview2 v1.0.22, found {webview_version or 'local replacement'}")
    webview_dir = Path(
        subprocess.check_output(
            ["go", "list", "-m", "-f", "{{.Dir}}", WEBVIEW2], cwd=ROOT, text=True
        ).strip()
    )
    chromium_source = webview_dir / "pkg/edge/chromium.go"
    if hashlib.sha256(chromium_source.read_bytes()).hexdigest() != WEBVIEW2_CHROMIUM_HASH:
        raise RuntimeError("go-webview2 Chromium source changed; audit before updating guard")
    for relative, expected_hash in SOURCE_HASHES.items():
        original = module_dir / relative
        source = original.read_bytes()
        if hashlib.sha256(source).hexdigest() != expected_hash:
            raise RuntimeError(f"Wails source changed: {relative}; audit before updating guard")
    patched = tmp / "wails"
    shutil.copytree(module_dir, patched)
    patched_webview = tmp / "go-webview2"
    shutil.copytree(webview_dir, patched_webview)
    chromium = patched_webview / "pkg/edge/chromium.go"
    content = chromium.read_text(encoding="utf-8")
    webview_anchors = {
        "navigationCompleted              *ICoreWebView2NavigationCompletedEventHandler\n": "navigationCompleted              *ICoreWebView2NavigationCompletedEventHandler\n\tnavigationStarting               *ateneaNavigationHandler\n\tnewWindowRequested               *ateneaNewWindowHandler\n",
        "e.navigationCompleted = newICoreWebView2NavigationCompletedEventHandler(e)\n": "e.navigationCompleted = newICoreWebView2NavigationCompletedEventHandler(e)\n\te.navigationStarting = &ateneaNavigationHandler{vtbl: &ateneaNavigationVtable, owner: e}\n\te.newWindowRequested = &ateneaNewWindowHandler{vtbl: &ateneaNewWindowVtable, owner: e}\n",
    }
    for anchor, replacement in webview_anchors.items():
        if content.count(anchor) != 1:
            raise RuntimeError("go-webview2 Chromium navigation anchor changed")
        content = content.replace(anchor, replacement)
    registration = "err = e.webview.AddNavigationCompleted(e.navigationCompleted, &token)\n\tif err != nil {\n\t\te.errorCallback(err)\n\t}\n"
    if content.count(registration) != 1:
        raise RuntimeError("go-webview2 Chromium registration anchor changed")
    content = content.replace(registration, registration + "\te.ateneaRegisterNavigation()\n")
    chromium.parent.chmod(0o700)
    chromium.chmod(0o600)
    chromium.write_text(content, encoding="utf-8")
    subprocess.run(["gofmt", "-w", str(chromium)], check=True)
    for filename in ("navigation_windows.go.txt", "navigation_windows_test.go.txt"):
        target = patched_webview / "pkg/edge" / filename.removesuffix(".txt").replace("navigation_windows", "atenea_navigation")
        target.write_text((ROOT / "bridge" / filename).read_text(encoding="utf-8"), encoding="utf-8")
        subprocess.run(["gofmt", "-w", str(target)], check=True)
    for relative in SOURCE_HASHES:
        target = patched / relative
        content = target.read_text(encoding="utf-8")
        if relative.endswith("dispatcher.go"):
            import_anchor = 'import (\n\t"context"\n'
            function_anchor = 'func (d *Dispatcher) ProcessMessage(message string, sender frontend.Frontend) (_ string, err error) {\n'
            if content.count(import_anchor) != 1 or content.count(function_anchor) != 1:
                raise RuntimeError("Wails dispatcher anchors changed")
            content = content.replace(import_anchor, import_anchor + '\t"encoding/json"\n')
            content = content.replace(function_anchor, function_anchor + GUARD_CALL)
            content += (ROOT / "bridge/guard.go.txt").read_text(encoding="utf-8")
        elif relative.endswith("frontend.go"):
            if "darwin" in relative or "linux" in relative:
                anchor = "func (f *Frontend) processMessage(message string) {\n"
            else:
                anchor = "func (f *Frontend) processMessage(message string, sender *edge.ICoreWebView2, args *edge.ICoreWebView2WebMessageReceivedEventArgs) {\n"
            if content.count(anchor) != 1:
                raise RuntimeError(f"Wails ingress anchor changed: {relative}")
            content = content.replace(anchor, anchor + "\tif !ateneaSSHIngressMessage(message) { return }\n")
            content += (ROOT / "bridge/ingress.go.txt").read_text(encoding="utf-8")
            if "windows" in relative:
                extra_anchor = "func (f *Frontend) processMessageWithAdditionalObjects(message string, sender *edge.ICoreWebView2, args *edge.ICoreWebView2WebMessageReceivedEventArgs) {\n"
                if content.count(extra_anchor) != 1:
                    raise RuntimeError("Wails Windows additional-objects anchor changed")
                content = content.replace(extra_anchor, extra_anchor + "\tif len(message) == 0 || message[0] != 'C' { return }\n")
        elif relative.endswith("WailsContext.m"):
            anchor = "- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)navigation {\n"
            if content.count(anchor) != 1:
                raise RuntimeError("Wails macOS navigation anchor changed")
            content = content.replace(anchor, (ROOT / "bridge/navigation_darwin.m.txt").read_text(encoding="utf-8") + anchor)
        elif relative.endswith("window.c"):
            function_anchor = "static void webviewLoadChanged(WebKitWebView *web_view, WebKitLoadEvent load_event, gpointer data)\n"
            signal_anchor = '    g_signal_connect(G_OBJECT(webview), "load-changed", G_CALLBACK(webviewLoadChanged), NULL);\n'
            if content.count(function_anchor) != 1 or content.count(signal_anchor) != 1:
                raise RuntimeError("Wails Linux navigation anchors changed")
            content = content.replace(function_anchor, (ROOT / "bridge/navigation_linux.c.txt").read_text(encoding="utf-8") + function_anchor)
            content = content.replace(signal_anchor, signal_anchor + '    g_signal_connect(G_OBJECT(webview), "decide-policy", G_CALLBACK(ateneaNavigationPolicy), NULL);\n')
        else:
            content += f'\n// {MARKER} proves this app was built with the reviewed Wails overlay.\nconst {MARKER} = "wails-v2.15.0-guard-v1"\n'
        target.parent.chmod(0o700)
        target.chmod(0o600)
        target.write_text(content, encoding="utf-8")
        if target.suffix == ".go":
            subprocess.run(["gofmt", "-w", str(target)], check=True)
    dispatcher_dir = patched / "internal/frontend/dispatcher"
    dispatcher_dir.chmod(0o700)
    (dispatcher_dir / "atenea_guard_test.go").write_text(
        (ROOT / "bridge/guard_test.go.txt").read_text(encoding="utf-8"), encoding="utf-8"
    )
    subprocess.run(["gofmt", "-w", str(dispatcher_dir / "atenea_guard_test.go")], check=True)
    for platform in ("darwin", "linux", "windows"):
        ingress_test = patched / f"internal/frontend/desktop/{platform}/atenea_ingress_test.go"
        ingress_test.write_text((ROOT / "bridge/ingress_test.go.txt").read_text(encoding="utf-8").replace("PLATFORM", platform, 1), encoding="utf-8")
        subprocess.run(["gofmt", "-w", str(ingress_test)], check=True)
    probe = tmp / "ingress-probe"
    probe.mkdir()
    (probe / "go.mod").write_text("module atenea-ssh-ingress-probe\n\ngo 1.25.0\n", encoding="utf-8")
    (probe / "ingress.go").write_text("package ingressprobe\n" + (ROOT / "bridge/ingress.go.txt").read_text(encoding="utf-8"), encoding="utf-8")
    (probe / "ingress_test.go").write_text(
        (ROOT / "bridge/ingress_test.go.txt").read_text(encoding="utf-8").replace("PLATFORM", "ingressprobe", 1), encoding="utf-8"
    )
    workspace = tmp / "go.work"
    workspace.write_text(
        "go 1.26.7\nuse (\n"
        + f"    {json.dumps(str(ROOT.parents[1]))}\n"
        + f"    {json.dumps(str(ROOT))}\n"
        + f"    {json.dumps(str(patched))}\n"
        + f"    {json.dumps(str(patched_webview))}\n"
        + ")\n", encoding="utf-8"
    )
    return workspace


def main() -> None:
    if len(sys.argv) != 2 or sys.argv[1] not in {"test", "build", "probe-build"}:
        raise SystemExit("usage: restricted_wails.py test|build|probe-build")
    with tempfile.TemporaryDirectory(prefix="atenea-wails-") as directory:
        workspace = prepare_workspace(Path(directory))
        env = dict(os.environ)
        env["GOWORK"] = str(workspace)
        if sys.argv[1] == "test":
            tags = ("-tags", "webkit2_41") if sys.platform.startswith("linux") else ()
            run("go", "test", "-count=1", WAILS + "/internal/frontend/dispatcher", env=env)
            if sys.platform == "win32":
                run("go", "test", "-count=1", WEBVIEW2 + "/pkg/edge", env=env)
            probe_env = dict(env)
            probe_env["GOWORK"] = "off"
            run("go", "test", "-count=1", "./...", cwd=Path(directory) / "ingress-probe", env=probe_env)
            run("go", "vet", *tags, "./...", env=env)
            run("go", "test", *tags, "./...", env=env)
        else:
            if sys.argv[1] == "probe-build":
                env["VITE_ATENEA_BRIDGE_PROBE"] = "1"
            else:
                env.pop("VITE_ATENEA_BRIDGE_PROBE", None)
            platform = env.pop("ATENEA_WAILS_PLATFORM", "")
            if platform and platform not in {"darwin/arm64", "windows/amd64", "windows/arm64", "linux/amd64"}:
                raise RuntimeError(f"unsupported Wails target: {platform}")
            build_args = ["go", "run", WAILS + "/cmd/wails", "build", "-clean"]
            if platform:
                build_args.extend(["-platform", platform])
            target_linux = platform.startswith("linux/") if platform else sys.platform.startswith("linux")
            build_args.extend(["-tags", "atenea_ssh_restricted" + (",webkit2_41" if target_linux else "")])
            run(
                *build_args,
                env=env,
            )
            assets = list((ROOT / "frontend/dist/assets").glob("*.js"))
            has_probe = any(b"Runtime no disponible" in asset.read_bytes() for asset in assets)
            if not assets or has_probe != (sys.argv[1] == "probe-build"):
                raise RuntimeError("frontend probe mode does not match requested build")


if __name__ == "__main__":
    main()
