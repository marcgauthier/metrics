#!/usr/bin/env python3
"""Build an independent focused source fork of the VictoriaMetrics storage engine.

Run once against the original pinned dependencies. Packages under
github.com/VictoriaMetrics/ are copied into this module's internal directory
and their imports rewritten. The original upstream modules are removed from
go.mod. Go packages from unrelated vendors remain standard Go dependencies.
"""
import glob
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

REPO = Path(__file__).resolve().parent.parent
MODULE = "github.com/marcgauthier/metrics"
PREFIX = "github.com/VictoriaMetrics/"
UPSTREAM_VERSION = "v1.150.0"

def run(*args):
    print("+", " ".join(args), flush=True)
    return subprocess.run(args, check=True, cwd=REPO, text=True, capture_output=True).stdout

def list_packages():
    raw = run("go", "list", "-deps", "-json", "./...")
    decoder = json.JSONDecoder()
    pos = 0
    while pos < len(raw):
        while pos < len(raw) and raw[pos].isspace():
            pos += 1
        if pos >= len(raw):
            break
        item, pos = decoder.raw_decode(raw, pos)
        yield item

def fork_name(path):
    name = path[len(PREFIX):].split("/")[0]
    return {"VictoriaLogs": "vl", "VictoriaMetrics": "vm"}.get(name, "vm-" + name.lower())

def remap(text, replacements):
    for old, new in sorted(replacements.items(), key=lambda item: -len(item[0])):
        text = text.replace('"' + old + '/', '"' + new + '/')
        text = text.replace('"' + old + '"', '"' + new + '"')
    return text

def copy_embedded_files(source_dir, target_dir, source_code):
    for match in re.finditer(r"(?m)^\s*//go:embed[ \t]+(.+?)\s*$", source_code):
        for expression in match.group(1).split():
            expression = expression.strip('"')
            if expression.startswith("all:"):
                expression = expression[4:]
            for file_path in glob.glob(str(source_dir / expression), recursive=True):
                src = Path(file_path)
                if not src.exists():
                    continue
                dst = target_dir / src.relative_to(source_dir)
                if src.is_dir():
                    shutil.copytree(src, dst, dirs_exist_ok=True,
                        ignore=shutil.ignore_patterns("testdata", ".git"))
                elif src.is_file():
                    dst.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copyfile(src, dst)

def main():
    os.chdir(REPO)
    packages = [p for p in list_packages()
                if p.get("ImportPath", "").startswith(PREFIX)
                and p.get("Module") and p.get("Dir")]
    if len(packages) < 5:
        raise RuntimeError("no complete upstream dependency graph was found")
    mappings = {}
    source_roots = {}
    for p in packages:
        module = p["Module"]
        path = module["Path"]
        if path.startswith(PREFIX):
            mappings[path] = MODULE + "/internal/" + fork_name(path)
            source_roots[path] = Path(module["Dir"])
    print("Copying", len(packages), "packages from", sorted(source_roots), flush=True)

    copies = 0
    for p in packages:
        name = p["ImportPath"]
        source_module = p["Module"]["Path"]
        if source_module not in mappings:
            continue
        suffix = name[len(source_module):].lstrip("/")
        source_dir = Path(p["Dir"])
        target_dir = REPO / "internal" / fork_name(source_module) / suffix
        target_dir.mkdir(parents=True, exist_ok=True)
        go_files = []
        for src in source_dir.iterdir():
            if src.is_dir() or src.name.startswith(".") or src.name.endswith("_test.go"):
                continue
            if src.suffix not in {".go", ".c", ".h", ".s", ".S", ".cc", ".cpp",
                                  ".syso", ".inc", ".proto", ".swig", ".swigcxx"}:
                continue
            dst = target_dir / src.name
            if src.suffix == ".go":
                content = remap(src.read_text(encoding="utf-8"), mappings)
                dst.write_text(content, encoding="utf-8")
                go_files.append(content)
            else:
                shutil.copyfile(src, dst)
            dst.chmod(0o644)
            copies += 1
        for content in go_files:
            copy_embedded_files(source_dir, target_dir, content)

    for module_path, source_dir in source_roots.items():
        target = REPO / "internal" / fork_name(module_path)
        target.mkdir(parents=True, exist_ok=True)
        for filename in ("LICENSE", "LICENSE.md", "LICENSE.txt", "NOTICE", "NOTICE.md"):
            src = source_dir / filename
            if src.is_file():
                dest = target / ("UPSTREAM_" + filename)
                shutil.copyfile(src, dest)
                dest.chmod(0o644)

    for source in list(REPO.glob("*.go")) + list((REPO / "cmd").rglob("*.go")):
        old = source.read_text(encoding="utf-8")
        changed = remap(old, mappings)
        if changed != old:
            source.write_text(changed, encoding="utf-8")

    module_spec = json.loads(run("go", "mod", "edit", "-json"))
    for requirement in module_spec.get("Require", []):
        name = requirement["Path"]
        if name.startswith(PREFIX):
            run("go", "mod", "edit", "-droprequire=" + name)

    notice = [
        "# Forked upstream code and licenses", "",
        "This package contains a focused, source-level fork of the VictoriaMetrics storage",
        "engine and the related VictoriaMetrics packages required by its",
        "in-process Go API. Packages are copied into internal/ and import paths",
        "are rewritten for the github.com/marcgauthier/metrics module.", "",
        "Upstream source: VictoriaMetrics " + UPSTREAM_VERSION + " and its pinned Go module graph.",
        "Copyright and license headers in upstream source are retained.",
        "The original module-level licenses are copied into each internal fork",
        "root as UPSTREAM_LICENSE, UPSTREAM_LICENSE.md, or UPSTREAM_NOTICE.", "",
        "Original module provenance:",
    ]
    for name in sorted(source_roots):
        notice.append("- " + name + " -> internal/" + fork_name(name) + "/")
    notice += ["",
        "Independent generic third-party Go dependencies, such as compression",
        "libraries, remain external Go modules. VictoriaMetrics/VictoriaLogs",
        "source packages are no longer downloaded during a build.", ""]
    (REPO / "FORK_NOTICES.md").write_text("\n".join(notice), encoding="utf-8")
    print("Copied", copies, "source files from", len(packages), "packages", flush=True)

if __name__ == "__main__":
    main()
