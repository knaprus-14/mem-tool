#!/usr/bin/env python3
"""Offline, administrator-only disaster recovery archive for mem-tool.

Stop every writer before backup. Restore always creates a NEW directory and
never overwrites a live installation. Absolute database/source paths are kept;
this is disaster recovery to the same location, not portable corpus migration.
Uses only Python's standard library; archive data can contain private documents
and database credentials. Do not publish it or place it under the web root.
"""
import argparse
from contextlib import closing, contextmanager
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import sqlite3
import shutil
import stat
import tempfile
import zipfile
import uuid

MAX_FILES = 100000
MAX_BYTES = 100 * 1024**3
MANIFEST = "mem-tool-backup.json"


@contextmanager
def database(path, **options):
    with closing(sqlite3.connect(path, **options)) as connection:
        with connection:
            yield connection


@contextmanager
def temporary_directory(prefix="mem-web-", parent=None):
    # On Windows inherit the user's TEMP/selected-parent ACL. Python's 0700
    # mkdir installs a new DACL that can exclude restricted process tokens.
    # POSIX keeps an explicit private directory. Never alter an existing ACL.
    parent = Path(parent or tempfile.gettempdir()).resolve(strict=True)
    folder = parent / (prefix + uuid.uuid4().hex)
    folder.mkdir(mode=0o777 if os.name == "nt" else 0o700)
    try:
        yield folder
    finally:
        resolved = folder.resolve(strict=True)
        if resolved.parent != parent or resolved != folder:
            raise ValueError("Temporary directory changed location; cleanup refused")
        shutil.rmtree(folder)


def sha256(path):
    h = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def check_db(path):
    # Read-only opens include committed WAL contents; no checkpoint or migration.
    with database(path.resolve().as_uri() + "?mode=ro", uri=True) as db:
        if db.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
            raise ValueError("SQLite integrity check failed: " + path.name)


def inventory(root):
    records = []
    total = 0
    for directory, dirs, names in os.walk(root, followlinks=False):
        for name in dirs + names:
            path = Path(directory) / name
            if path.is_symlink() or getattr(path.lstat(), "st_file_attributes", 0) & 0x400:
                raise ValueError("Links/reparse points are not supported in the data directory")
        for name in sorted(names):
            path = Path(directory) / name
            if not stat.S_ISREG(path.stat().st_mode):
                raise ValueError("Non-regular file in data directory")
            # These are process locks/temporary state, never recovery data.
            if name.endswith((".lock", "-shm")):
                continue
            relative = path.relative_to(root).as_posix()
            size = path.stat().st_size
            total += size
            if total > MAX_BYTES or len(records) >= MAX_FILES:
                raise ValueError("Backup exceeds configured file/size limits")
            if name.endswith(".db"):
                check_db(path)
            records.append({"path": relative, "size": size, "sha256": sha256(path)})
    return sorted(records, key=lambda record: record["path"])


def backup(data, output):
    root = Path(data).resolve(strict=True)
    output = Path(output).absolute()
    if not root.is_dir() or not (root / "catalog.db").is_file():
        raise ValueError("Expected mem-tool server data directory with catalog.db")
    if output.resolve().is_relative_to(root):
        raise ValueError("Archive must be outside the server data directory")
    if output.exists():
        raise ValueError("Output already exists; choose a new archive name")
    before = inventory(root)
    # Catalog paths are recorded without rewriting any provenance/history.
    with database((root / "catalog.db").as_uri() + "?mode=ro", uri=True) as db:
        paths = [row[0] for row in db.execute("SELECT root FROM web_workspaces")]
    if any(not Path(path).resolve().is_relative_to(root) for path in paths):
        raise ValueError("Catalog contains external workspaces; backup their data separately")
    manifest = {"format": 1, "original_data_dir": str(root), "files": before,
                "restore_contract": "same_absolute_paths", "contains_private_data": True}
    fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as stream:
            with zipfile.ZipFile(stream, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
                for record in before:
                    archive.write(root / record["path"], "data/" + record["path"])
                archive.writestr(MANIFEST, json.dumps(manifest, ensure_ascii=False, indent=2))
            stream.flush()
            os.fsync(stream.fileno())
        if inventory(root) != before:
            raise ValueError("Data changed during backup; stop all writers and retry")
        verify(output)
    except BaseException:
        # Only the exact file created by this invocation is removed.
        output.unlink(missing_ok=True)
        raise
    return manifest


def safe_name(name):
    p = PurePosixPath(name)
    devices = {"CON", "PRN", "AUX", "NUL"} | {f"{s}{i}" for s in ("COM", "LPT") for i in range(1, 10)}
    return (not p.is_absolute() and name == p.as_posix() and bool(p.parts)
            and all(part not in ("", ".", "..") and ":" not in part and "\\" not in part
                    and not part.endswith((".", " ")) and part.split(".")[0].upper() not in devices
                    and not any(ord(c) < 32 for c in part) for part in p.parts))


def unpack(archive_path, destination):
    destination = Path(destination)
    # This function is private: its caller owns an empty temporary directory.
    with zipfile.ZipFile(archive_path, "r") as archive:
        infos = archive.infolist()
        if len(infos) > MAX_FILES + 1 or sum(i.file_size for i in infos) > MAX_BYTES:
            raise ValueError("Archive exceeds limits")
        names = [i.filename for i in infos]
        if len(set(name.casefold() for name in names)) != len(names) or MANIFEST not in names:
            raise ValueError("Duplicate entries or missing manifest")
        info = archive.getinfo(MANIFEST)
        if info.file_size > 32 * 1024**2:
            raise ValueError("Manifest exceeds limit")
        manifest = json.loads(archive.read(MANIFEST))
        if manifest.get("format") != 1 or manifest.get("restore_contract") != "same_absolute_paths":
            raise ValueError("Unsupported backup format")
        records = manifest.get("files")
        if not isinstance(records, list) or len(records) > MAX_FILES:
            raise ValueError("Invalid file manifest")
        expected = {"data/" + record["path"]: record for record in records}
        if len(expected) != len(records) or set(names) != set(expected) | {MANIFEST}:
            raise ValueError("Manifest and archive differ")
        for name, record in expected.items():
            if not safe_name(record["path"]):
                raise ValueError("Unsafe archive path")
            item = archive.getinfo(name)
            if stat.S_ISLNK(item.external_attr >> 16) or item.is_dir() or item.file_size != record["size"]:
                raise ValueError("Invalid archived file")
            target = destination.joinpath(*PurePosixPath(record["path"]).parts)
            target.parent.mkdir(parents=True, exist_ok=True, mode=0o777 if os.name == "nt" else 0o700)
            digest = hashlib.sha256()
            with archive.open(item) as source, target.open("xb") as out:
                os.chmod(target, 0o600)
                for block in iter(lambda: source.read(1024 * 1024), b""):
                    digest.update(block)
                    out.write(block)
            if digest.hexdigest() != record["sha256"]:
                raise ValueError("SHA-256 mismatch")
        for record in records:
            if record["path"].endswith(".db"):
                check_db(destination / record["path"])
    return manifest


def verify(archive):
    with temporary_directory(prefix="mem-web-verify-") as directory:
        return unpack(archive, directory)


def restore(archive, destination):
    target = Path(destination).absolute()
    if target.exists():
        raise ValueError("Restore target must not exist; live data will never be replaced")
    if not target.parent.is_dir():
        raise ValueError("Restore parent directory must exist")
    # Verified staging is on the destination filesystem, followed by one rename.
    with temporary_directory(prefix=".mem-web-restore-", parent=target.parent) as directory:
        stage = Path(directory) / "data"
        stage.mkdir(mode=0o777 if os.name == "nt" else 0o700)
        manifest = unpack(archive, stage)
        if target.exists():
            raise ValueError("Restore target appeared during verification")
        stage.rename(target)
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    create = sub.add_parser("backup")
    create.add_argument("--data", required=True)
    create.add_argument("--output", required=True)
    create.add_argument("--writers-stopped", action="store_true", required=True,
                        help="Confirm all service/CLI/worker writers have been stopped")
    check = sub.add_parser("verify")
    check.add_argument("archive")
    load = sub.add_parser("restore")
    load.add_argument("archive")
    load.add_argument("--destination", required=True)
    args = parser.parse_args()
    if args.command == "backup":
        result = backup(args.data, args.output)
    elif args.command == "verify":
        result = verify(args.archive)
    else:
        result = restore(args.archive, args.destination)
    print(json.dumps({"status": "verified", "files": len(result["files"]),
                      "restore_contract": result["restore_contract"]}))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, sqlite3.Error, zipfile.BadZipFile, KeyError, TypeError) as exc:
        raise SystemExit("Backup/restore failed: " + str(exc))
