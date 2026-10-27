#!/usr/bin/env python3
"""Package the CPA store's single root library, checksums, and direct registry."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import stat
from urllib.parse import urlsplit
import zipfile

EXTENSIONS = {"linux/amd64": "so", "linux/arm64": "so", "darwin/amd64": "dylib",
              "darwin/arm64": "dylib", "windows/amd64": "dll"}
PLUGIN = "cpa-model-dock"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", default="0.1.0")
    parser.add_argument("--dist", type=Path, default=Path("dist"))
    parser.add_argument("--base-url", default="https://example.invalid/cpa-model-dock/0.1.0")
    parser.add_argument("--platforms", nargs="+", default=list(EXTENSIONS))
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9][0-9A-Za-z.+-]*", args.version):
        parser.error("version must start with a digit and contain only version characters")
    url = urlsplit(args.base_url)
    if url.scheme not in ("https", "http") or not url.netloc or url.query or url.fragment:
        parser.error("base-url must be an HTTP(S) directory URL without query or fragment")
    platforms = sorted(set(args.platforms))
    libraries = []
    for platform in platforms:
        if platform not in EXTENSIONS:
            parser.error(f"unsupported platform: {platform}")
        library = args.dist / platform / f"{PLUGIN}.{EXTENSIONS[platform]}"
        if not library.is_file() or library.is_symlink():
            parser.error(f"missing regular library: {library}; run make dist first")
        libraries.append((platform, library))
    artifacts, checksums = [], []
    for platform, library in libraries:
        goos, goarch = platform.split("/")
        name = f"{PLUGIN}_{args.version}_{goos}_{goarch}.zip"
        archive = args.dist / name
        temporary = archive.with_suffix(".zip.tmp")
        info = zipfile.ZipInfo(library.name, (1980, 1, 1, 0, 0, 0))
        info.create_system = 3
        info.external_attr = (stat.S_IFREG | 0o755) << 16
        info.compress_type = zipfile.ZIP_DEFLATED
        with zipfile.ZipFile(temporary, "w", compression=zipfile.ZIP_DEFLATED,
                             compresslevel=9) as writer:
            writer.writestr(info, library.read_bytes())
        temporary.replace(archive)
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        line = f"{digest}  {name}\n"
        (args.dist / (name + ".sha256")).write_text(line)
        checksums.append(line)
        artifacts.append({"goos": goos, "goarch": goarch,
                          "url": args.base_url.rstrip("/") + "/" + name,
                          "sha256": digest, "size": archive.stat().st_size})
        print(f"{name}: {archive.stat().st_size} bytes, sha256 {digest}")
    checksum_text = "".join(sorted(checksums, key=lambda line: line.split("  ")[1]))
    (args.dist / "SHA256SUMS").write_text(checksum_text)
    # CPA's GitHub-release installer specifically selects checksums.txt.
    (args.dist / "checksums.txt").write_text(checksum_text)
    registry = {"schema_version": 2, "plugins": [{
        "id": PLUGIN, "name": "CPAModelDock",
        "description": "Model aliases, per-alias version updates, and a model whitelist",
        "author": "CPAModelDock contributors", "version": args.version, "license": "MIT",
        "tags": ["models", "aliases", "whitelist"],
        "install": {"type": "direct", "artifacts": artifacts}}]}
    (args.dist / "registry.json").write_text(json.dumps(registry, indent=2) + "\n")
    if url.hostname == "example.invalid":
        print("registry.json is a sample: regenerate with BASE_URL pointing at your HTTPS host.")


if __name__ == "__main__":
    main()
