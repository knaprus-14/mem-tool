import importlib.util
import json
from pathlib import Path
import sqlite3
import unittest
import zipfile

spec = importlib.util.spec_from_file_location("web_backup", Path(__file__).with_name("web_backup.py"))
backup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(backup)


class RecoveryTests(unittest.TestCase):
    def fixture(self, folder):
        root = Path(folder) / "server"
        root.mkdir()
        workspace = root / "workspaces" / "alice" / "one"
        data = workspace / ".mem"
        (data / "originals").mkdir(parents=True)
        with backup.database(root / "catalog.db") as db:
            db.execute("CREATE TABLE web_workspaces(root TEXT)")
            db.execute("INSERT INTO web_workspaces VALUES(?)", (str(workspace),))
        with backup.database(data / "store.db") as db:
            db.execute("CREATE TABLE entries(id INTEGER PRIMARY KEY, text TEXT)")
            db.execute("INSERT INTO entries VALUES(1,'Текст документа')")
        (data / "originals" / "sample.md").write_text("Original", encoding="utf-8")
        (data / "config.json").write_text('{"backend":"ollama"}')
        return root

    def test_roundtrip_and_refuse_existing_target(self):
        with backup.temporary_directory() as folder:
            root = self.fixture(folder)
            archive = Path(folder) / "copy.zip"
            manifest = backup.backup(root, archive)
            self.assertEqual(len(manifest["files"]), 4)
            self.assertEqual(backup.verify(archive), manifest)
            destination = Path(folder) / "restored"
            backup.restore(archive, destination)
            self.assertEqual(backup.inventory(root), backup.inventory(destination))
            with self.assertRaises(ValueError):
                backup.restore(archive, root)
            with self.assertRaises(ValueError):
                backup.backup(root, archive)
            with self.assertRaises(ValueError):
                backup.backup(root, root / "self.zip")

    def test_committed_wal_is_included(self):
        with backup.temporary_directory() as folder:
            root = self.fixture(folder)
            path = root / "workspaces" / "alice" / "one" / ".mem" / "store.db"
            connection = sqlite3.connect(path)
            try:
                connection.execute("PRAGMA journal_mode=WAL")
                connection.execute("INSERT INTO entries VALUES(2,'Committed in WAL')")
                connection.commit()
                self.assertTrue(Path(str(path) + "-wal").exists())
                archive = Path(folder) / "wal.zip"
                manifest = backup.backup(root, archive)
                self.assertTrue(any(r["path"].endswith("-wal") for r in manifest["files"]))
                destination = Path(folder) / "restore"
                backup.restore(archive, destination)
                with backup.database(destination / path.relative_to(root)) as db:
                    self.assertEqual(db.execute("SELECT text FROM entries WHERE id=2").fetchone()[0], "Committed in WAL")
            finally:
                connection.close()

    def test_traversal_and_hash_corruption_leave_no_target(self):
        with backup.temporary_directory() as folder:
            root = self.fixture(folder)
            original = Path(folder) / "good.zip"
            backup.backup(root, original)
            for damage in ("hash", "traversal", "duplicate"):
                bad = Path(folder) / (damage + ".zip")
                with zipfile.ZipFile(original) as source, zipfile.ZipFile(bad, "w") as target:
                    for info in source.infolist():
                        data = source.read(info.filename)
                        if info.filename == backup.MANIFEST and damage == "hash":
                            m = json.loads(data)
                            m["files"][0]["sha256"] = "0" * 64
                            data = json.dumps(m).encode()
                        target.writestr(info.filename, data)
                    if damage == "traversal":
                        target.writestr("../escape", "bad")
                    if damage == "duplicate":
                        target.writestr("MEM-TOOL-BACKUP.JSON", "{}")
                destination = Path(folder) / (damage + "-restored")
                with self.assertRaises(ValueError):
                    backup.restore(bad, destination)
                self.assertFalse(destination.exists())
            self.assertFalse((Path(folder) / "escape").exists())

    def test_corrupt_db_and_external_catalog_rejected(self):
        with backup.temporary_directory() as folder:
            root = self.fixture(folder)
            with backup.database(root / "catalog.db") as db:
                db.execute("INSERT INTO web_workspaces VALUES(?)", (str(Path(folder) / "external"),))
            archive = Path(folder) / "copy.zip"
            with self.assertRaises(ValueError):
                backup.backup(root, archive)
            self.assertFalse(archive.exists())
            (root / "catalog.db").write_bytes(b"not a database")
            with self.assertRaises(sqlite3.DatabaseError):
                backup.backup(root, archive)


if __name__ == "__main__":
    unittest.main()
