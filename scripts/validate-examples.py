#!/usr/bin/env python3
"""Validate/save the four drafts through an isolated real CPA (no paid requests)."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

BASE = "/v0/management/plugins/cpa-model-dock"
MODELS = ["gpt-6.1-sol", "gpt-6.9-sol", "gpt-6.10-sol", "gpt-7-sol-preview",
          "gpt-6.1-sol-mini", "gpt-6-astra", "claude-opus-4-5", "unrelated-model"]


def main():
    root = Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpa-bin", default=os.environ.get("CPA_BIN", "/tmp/cpa-bin"))
    extension = {"darwin": "dylib", "linux": "so", "win32": "dll"}.get(sys.platform, "so")
    parser.add_argument("--library", type=Path, default=root / f"dist/cpa-model-dock.{extension}")
    parser.add_argument("--keep", action="store_true", help="retain isolated config/log/state/results")
    args = parser.parse_args()
    binary = Path(args.cpa_bin).resolve()
    library = args.library.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK) or not library.is_file():
        parser.error("provide an executable CPA binary and a built host plugin library")
    directory = Path(tempfile.mkdtemp(prefix="cpa-model-dock-examples-"))
    for name in ("auth", "state", "plugins"):
        (directory / name).mkdir(mode=0o700)
    shutil.copyfile(library, directory / "plugins" / ("cpa-model-dock" + library.suffix))
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    key = secrets.token_hex(24)
    config = {
        "config-version": 8, "server": {"host": "127.0.0.1", "port": port},
        "management": {"secret-key": key, "allow-remote": False,
                       "disable-control-panel": True, "disable-auto-update-panel": True},
        "access": {"api-keys": [secrets.token_hex(24)]},
        "oauth": {"auth-dir": str(directory / "auth")},
        "api-keys": {"openai-compatibility": [{
            "name": "example-validation", "base-url": "http://127.0.0.1:9/v1",
            "keys": [{"api-key": "fixture-only-not-a-real-secret", "proxy-url": "direct"}],
            "models": [{"name": model, "alias": model} for model in MODELS]}]},
        "plugins": {"enabled": True, "dir": str(directory / "plugins"), "configs": {
            "cpa-model-dock": {"enabled": True, "priority": 1000,
                               "state-dir": str(directory / "state"),
                               "catalog-refresh-interval": "5m", "guard-uncovered-endpoints": True}}}}
    config_file = directory / "config.yaml"
    config_file.write_text(json.dumps(config, indent=2) + "\n")
    config_file.chmod(0o600)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def call(method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(f"http://127.0.0.1:{port}" + path, data=data,
                                         method=method, headers={"Authorization": "Bearer " + key,
                                                                 "Content-Type": "application/json"})
        try:
            with opener.open(request, timeout=5) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"{method} {path}: HTTP {error.code}: {error.read(4096).decode()}") from None

    success = False
    process = None
    try:
        with (directory / "cpa.log").open("wb") as log:
            env = {"PATH": os.environ.get("PATH", ""), "HOME": str(directory),
                   "TMPDIR": str(directory), "NO_PROXY": "*", "LANG": "C"}
            process = subprocess.Popen([str(binary), "-config", str(config_file), "-local-model"],
                                       cwd=directory, env=env, stdout=log, stderr=log)
            deadline = time.monotonic() + 30
            while True:
                if process.poll() is not None:
                    raise RuntimeError("CPA exited before plugin readiness; inspect cpa.log")
                try:
                    overview = call("GET", BASE + "/overview")
                    break
                except (OSError, RuntimeError):
                    if time.monotonic() >= deadline:
                        raise RuntimeError("Timed out waiting for plugin management API") from None
                    time.sleep(0.15)
            state = call("GET", BASE + "/state")
            catalog = call("POST", BASE + "/catalog/refresh", {"revision": state["revision"]})
            catalog_ids = {model["id"] for model in catalog["models"]}
            if not set(MODELS).issubset(catalog_ids):
                raise RuntimeError("Configured fixture models are missing from plugin catalog")
            results = []
            for example in sorted((root / "examples").glob("[0-9][0-9]-*.json")):
                draft = json.loads(example.read_text())
                validation = call("POST", BASE + "/validate", draft)
                if validation.get("valid") is not True:
                    raise RuntimeError(f"{example.name}: validation failed: {validation}")
                current = call("GET", BASE + "/state")
                draft["revision"] = current["revision"]
                saved = call("PUT", BASE + "/state", draft)
                # Read back the actual durable policy; do not mistake validation for a save.
                persisted = json.loads((directory / "state/state.json").read_text())
                if persisted["revision"] != saved["revision"]:
                    raise RuntimeError(f"{example.name}: saved revision was not persisted")
                result = {"example": example.name, "valid": True,
                          "saved_revision": saved["revision"], "preview": validation["preview"]}
                if example.name.startswith("02"):
                    evaluation = call("POST", BASE + "/aliases/check", {"dry_run": True})
                    result["auto_dry_run"] = evaluation
                if example.name.startswith("04"):
                    explanation = call("POST", BASE + "/explain", {"model": "gpt-6.1-sol"})
                    if explanation.get("allowed") is not False or explanation.get("code") != "hidden_by_alias":
                        raise RuntimeError("aliases-only example did not hide the original ID")
                    preview = validation["preview"]
                    if preview["public_models"] or set(preview["public_aliases"]) - {"gpt-sol"}:
                        raise RuntimeError(f"aliases-only preview exposed an unexpected model: {preview}")
                    if preview["public_aliases"] != ["gpt-sol"]:
                        result["runtime_note"] = "Alias omitted by runtime catalog; validation/save is not proof of routability"
                    result["original_explain"] = explanation
                results.append(result)
                print(f"PASS {example.name}: POST /validate valid:true; PUT /state revision {saved['revision']}")
            report = {"cpa_binary": str(binary), "library": str(library), "overview": overview,
                      "fixture_models": MODELS, "catalog": catalog, "results": results,
                      "scope": "Management validation/save/preview only; no inference or paid probes"}
            (directory / "results.json").write_text(json.dumps(report, indent=2) + "\n")
            success = True
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        if args.keep or not success:
            print(f"Artifacts retained: {directory}")
        else:
            shutil.rmtree(directory)


if __name__ == "__main__":
    main()
