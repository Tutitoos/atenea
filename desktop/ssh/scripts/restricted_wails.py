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
SOURCE_HASHES = {
    "internal/frontend/dispatcher/dispatcher.go": "baa6bc120411c07323e66476bb14a9872088a970449a600a765890012beb3133",
    "internal/frontend/desktop/darwin/frontend.go": "96b4e064ea8178a0ae26e65eab5c92c4200eca3f0f241c2035f88de6eaead35e",
    "internal/frontend/desktop/linux/frontend.go": "2610e740979055e636a30d05670126aad3fa6b77071c3f9af20e2fb6c47c89f5",
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
    for relative, expected_hash in SOURCE_HASHES.items():
        original = module_dir / relative
        source = original.read_bytes()
        if hashlib.sha256(source).hexdigest() != expected_hash:
            raise RuntimeError(f"Wails source changed: {relative}; audit before updating guard")
    patched = tmp / "wails"
    shutil.copytree(module_dir, patched)
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
        else:
            content += f'\n// {MARKER} proves this app was built with the reviewed Wails overlay.\nconst {MARKER} = "wails-v2.15.0-guard-v1"\n'
        target.parent.chmod(0o700)
        target.chmod(0o600)
        target.write_text(content, encoding="utf-8")
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
            has_probe = any(b"Puente bloqueado" in asset.read_bytes() for asset in assets)
            if not assets or has_probe != (sys.argv[1] == "probe-build"):
                raise RuntimeError("frontend probe mode does not match requested build")


if __name__ == "__main__":
    main()
