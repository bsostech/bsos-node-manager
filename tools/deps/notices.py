#!/usr/bin/env python3
"""Generate third-party license notices and dependency lists: third_party/NOTICES-{backend,agent,frontend}.txt, third_party/DEPENDENCIES.md.

Only covers code that "gets distributed":
- Go: modules actually compiled into binaries (build each cmd once, read `go version -m`), both Core and Enterprise; plus the Go standard library license.
- Frontend: non-dev packages in package-lock (they go into the bundle), plus the msw service worker that public/ copies verbatim into dist.
Font OFL licenses are in third_party/fonts/ (not regenerated here).

    python3 tools/deps/notices.py          # regenerate
    python3 tools/deps/notices.py --check  # compare with the repo copy; fail on any difference or a disallowed license (CI / before sending a PR)
"""
import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "third_party"
# Licenses allowed for distribution; anything not here (GPL, AGPL, LGPL, unclear license) must be discussed before adding
ALLOWED = {"MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "ISC", "MPL-2.0", "0BSD", "OFL-1.1"}
# The open edition is exported without Enterprise source but keeps the lists generated from the full repo (a few extra modules listed do no harm)
HAS_ENTERPRISE = (ROOT / "backend/enterprise").is_dir()
LICENSE_FILE = re.compile(r"^(LICEN[CS]E|COPYING|NOTICE)(\.(md|txt))?$", re.I)

GO_BUILDS = [  # (product, module dir, package, build tags)
    ("backend", "backend", "./cmd/server", ""),
    ("backend", "backend", "./cmd/server", "enterprise"),
    ("backend", "backend", "./cmd/cactl", ""),
    ("agent", "agent", "./cmd/agent", ""),
]


def sh(*args, cwd=ROOT):
    return subprocess.run(args, cwd=cwd, capture_output=True, text=True, check=True).stdout


def classify(text):
    """Identify the SPDX id from the full license text; return None if unrecognized (left for the ALLOWED check to block)."""
    t = " ".join(text.split())
    # A file often carries both the main license and sub-component licenses (e.g. BSD first, then Apache appended): take the earliest one as the main license
    marks = []
    for mark, spdx in (("Apache License", "Apache-2.0"), ("Mozilla Public License", "MPL-2.0"),
                       ("Permission is hereby granted, free of charge", "MIT"),
                       ("Redistribution and use in source and binary forms", "BSD"),
                       ("Permission to use, copy, modify, and/or distribute this software for any purpose", "ISC/0BSD"),
                       ("Permission to use, copy, modify, and distribute this software for any", "ISC")):
        i = t.find(mark)
        if i >= 0:
            marks.append((i, spdx))
    if not marks:
        return None
    spdx = min(marks)[1]
    if spdx == "BSD":
        return "BSD-3-Clause" if ("endorse or promote" in t or "neither the name" in t.lower()) else "BSD-2-Clause"
    if spdx == "ISC/0BSD":
        return "0BSD" if "copyright notice and this permission notice appear" not in t else "ISC"
    return spdx


SKIP_DIRS = {"testdata", "node_modules", ".git", "_examples", "examples"}


def license_texts(d, deep=True):
    d = Path(d)
    files = [p for p in (d.rglob("*") if deep else d.iterdir())
             if p.is_file() and LICENSE_FILE.match(p.name) and not SKIP_DIRS & set(p.relative_to(d).parts)]
    files.sort(key=lambda p: (len(p.relative_to(d).parts), str(p)))  # root-level files first; classify looks at the first one
    return [(str(p.relative_to(d)), p.read_text(errors="replace").strip()) for p in files]


def go_modules():
    """{product: {(module, version): module dir}}"""
    out = {"backend": {}, "agent": {}}
    with tempfile.TemporaryDirectory() as tmp:
        for i, (product, mod, pkg, tags) in enumerate(GO_BUILDS):
            if tags == "enterprise" and not HAS_ENTERPRISE:
                continue
            binp = os.path.join(tmp, f"b{i}")
            # Build for the platforms actually distributed: platform-specific deps (e.g. ones gopsutil pulls only on macOS / Windows) do not count
            subprocess.run(["go", "build", "-tags", tags, "-o", binp, pkg], cwd=ROOT / mod, check=True, capture_output=True,
                           env={**os.environ, "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"})
            for line in sh("go", "version", "-m", binp).splitlines():
                f = line.split()
                if f and f[0] in ("dep", "=>"):
                    out[product].setdefault((f[1], f[2]), mod)
    res = {}
    for product, mods in out.items():
        res[product] = {}
        for (m, v), moddir in sorted(mods.items()):
            info = json.loads(sh("go", "mod", "download", "-json", f"{m}@{v}", cwd=ROOT / moddir))
            res[product][(m, v)] = info["Dir"]
    return res


def npm_packages():
    fe = ROOT / "frontend"
    lock = json.loads((fe / "package-lock.json").read_text())
    pkgs = {}
    for path, meta in lock["packages"].items():
        if not path or meta.get("dev") or meta.get("devOptional"):
            continue
        name = path.split("node_modules/")[-1]
        pkgs[(name, meta["version"])] = (fe / path, meta.get("license"))
    # public/ goes into dist verbatim: the mock service worker is a devDependency, but the file still ships with the frontend
    msw = fe / "node_modules/msw"
    pkgs[("msw (public/mockServiceWorker.js)", json.loads((msw / "package.json").read_text())["version"])] = (msw, "MIT")
    return pkgs


def render(product, entries, extra_header=""):
    """entries: [(name, version, spdx, [(file, text)])]"""
    lines = [f"Third-party software in the BSOS Node Manager {product}", "",
             "This file lists the third-party components distributed with this part of the project and",
             "reproduces their license notices. It is generated by tools/deps/notices.py; do not edit by hand.", ""]
    if extra_header:
        lines += [extra_header, ""]
    for name, ver, spdx, texts in entries:
        lines += ["=" * 78, f"{name} {ver}", f"License: {spdx}", "-" * 78]
        for fname, text in texts:
            lines += ([f"[{fname}]"] if len(texts) > 1 else []) + [text, ""]
    return "\n".join(lines).rstrip() + "\n"


def build():
    problems, files, rows = [], {}, {}
    gomods = go_modules()
    for product, mods in gomods.items():
        # Ask from the module dir: the toolchain go.mod specifies is the one that actually builds the binary
        goroot = sh("go", "env", "GOROOT", cwd=ROOT / product).strip()
        gover = "go" + sh("go", "list", "-m", "-f", "{{.GoVersion}}", cwd=ROOT / product).strip()
        entries = [("Go standard library", gover, "BSD-3-Clause",
                    [("LICENSE", Path(goroot, "LICENSE").read_text().strip())])]
        for (m, v), d in mods.items():
            texts = license_texts(d)
            spdx = next((s for s in (classify(t) for _, t in texts[:1]) if s), None) or \
                next((s for s in (classify(t) for _, t in texts) if s), None)
            if not texts or spdx not in ALLOWED:
                problems.append(f"{product}: {m} {v} license {spdx or 'not recognized'} ({d})")
            entries.append((m, v, spdx or "UNKNOWN", texts))
            rows.setdefault((m, v, spdx or "UNKNOWN"), set()).add(product)
        files[f"NOTICES-{product}.txt"] = render(product, entries)
    # The agent binary is distributed separately from the platform image (download, offline install bundle), so the notices travel with it via go:embed
    files["../agent/cmd/agent/NOTICES.txt"] = files["NOTICES-agent.txt"]
    entries = []
    for (name, ver), (d, declared) in sorted(npm_packages().items()):
        texts = license_texts(d, deep=False)
        spdx = declared or next((s for s in (classify(t) for _, t in texts) if s), None)
        if spdx not in ALLOWED:
            problems.append(f"frontend: {name} {ver} license {spdx or 'not recognized'}")
        if not texts:  # a few packages ship no license file (declared only in package.json); record what is declared rather than leave it blank
            texts = [("package.json", f"Declared license: {spdx}. No license file is included in the package.")]
        entries.append((name, ver, spdx or "UNKNOWN", texts))
        rows.setdefault((name, ver, spdx or "UNKNOWN"), set()).add("frontend")
    files["NOTICES-frontend.txt"] = render("web UI", entries, "Fonts are listed in NOTICE; their licenses are in third_party/fonts/.")
    md = ["# Third-party dependencies", "",
          "Third-party code that is distributed: for the backend and the agent, the Go modules compiled into the binaries; for the frontend, the npm packages that go into the bundle.",
          "Generated by `tools/deps/notices.py`; do not edit by hand. Full license texts are in the `NOTICES-*.txt` files next to this one. See `tools/deps/README.md` for how this is maintained.", "",
          "| Package | Version | License | Distributed in |", "|---|---|---|---|"]
    md += [f"| `{n}` | {v} | {s} | {', '.join(sorted(p))} |" for (n, v, s), p in sorted(rows.items())]
    files["DEPENDENCIES.md"] = "\n".join(md) + "\n"
    return files, problems


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="compare with the files in the repo instead of writing them")
    a = ap.parse_args()
    files, problems = build()
    stale = [n for n, c in files.items() if not (OUT / n).exists() or (OUT / n).read_text() != c]
    if not HAS_ENTERPRISE:  # without the Enterprise modules the backend list differs anyway; compare only agent and frontend
        stale = [n for n in stale if n not in ("NOTICES-backend.txt", "DEPENDENCIES.md")]
    if not a.check:
        if not HAS_ENTERPRISE:
            sys.exit("backend/enterprise is missing: the maintainers regenerate these lists; use --check to compare")
        for n, c in files.items():
            (OUT / n).write_text(c)
        stale = []
    for p in problems:
        print("license needs attention:", p, file=sys.stderr)
    for n in stale:
        print(f"third_party/{n} is out of sync with the current dependencies; rerun python3 tools/deps/notices.py", file=sys.stderr)
    sys.exit(1 if problems or stale else 0)


if __name__ == "__main__":
    main()
