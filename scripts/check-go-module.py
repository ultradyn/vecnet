#!/usr/bin/env python3
"""Build the distributed module through an isolated file proxy, including embeds.

No tag, package, credential or service is published/modified. Run on a build box.
"""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import zipfile

root = Path(__file__).resolve().parent.parent
module = "github.com/XertroV/vecnet"
escaped = "".join("!" + c.lower() if c.isupper() else c for c in module)
files = subprocess.check_output(["rg", "--files", "--hidden", "-g", "!.git/**"], cwd=root, text=True).splitlines()
digest = hashlib.sha256()
for name in sorted(files):
    path = root / name
    if path.is_file() and not path.is_symlink():
        digest.update(name.encode())
        digest.update(path.read_bytes())
version = "v0.0.0-issue71." + digest.hexdigest()[:12]
with tempfile.TemporaryDirectory(prefix="vecnet-go-module-") as temporary:
    temporary = Path(temporary)
    proxy = temporary / "proxy"
    versions = proxy / escaped / "@v"
    versions.mkdir(parents=True)
    (versions / "list").write_text(version + "\n")
    (versions / (version + ".info")).write_text(json.dumps({"Version": version, "Time": "2026-11-15T00:00:00Z"}))
    (versions / (version + ".mod")).write_bytes((root / "go.mod").read_bytes())
    with zipfile.ZipFile(versions / (version + ".zip"), "w", compression=zipfile.ZIP_DEFLATED) as archive:
        for name in sorted(files):
            path = root / name
            if path.is_file() and not path.is_symlink():
                archive.write(path, module + "@" + version + "/" + name)
    environment = dict(os.environ, GOPROXY=proxy.as_uri() + ",https://proxy.golang.org", GOSUMDB="off", GONOSUMDB=module,
                       GONOPROXY="none", GOPRIVATE="", GOBIN=str(temporary / "bin"),
                       GOWORK="off", CGO_ENABLED="0", GOMAXPROCS="2")
    subprocess.run(["go", "install", "-p", "2", module + "/cmd/vecnet@" + version,
                    module + "/cmd/vecnet-hub@" + version], cwd=temporary, env=environment, check=True)
    subprocess.run([str(temporary / "bin/vecnet"), "help"], cwd=temporary, env=environment, check=True, stdout=subprocess.DEVNULL)
    print("PASS: isolated versioned Go installation includes embedded runtime assets")
