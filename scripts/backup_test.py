#!/usr/bin/env python3
"""The backup script must produce a database that opens and holds the rows.

Run by scripts/ci.sh. It writes to a temp dir and never touches the real one.
"""
import os
import sqlite3
import subprocess
import sys
import tempfile
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "xellter-backup.py"


def main() -> int:
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        data = tmp / "data"
        data.mkdir()
        (data / "secrets.env").write_text("KENARI_KEY_X=kn-x\n")
        (tmp / "xellter.yaml").write_text("clients: []\n")

        db = data / "xellter.db"
        con = sqlite3.connect(db)
        con.execute("CREATE TABLE requests(id INTEGER PRIMARY KEY, ts INTEGER, client TEXT)")
        con.executemany("INSERT INTO requests(ts, client) VALUES(?,?)",
                         [(1700000000000 + i, "demo") for i in range(5)])
        con.commit()
        con.close()

        out = tmp / "backups"
        r = subprocess.run([sys.executable, str(SCRIPT), str(data), str(out), "3"],
                           capture_output=True, text=True)
        if r.returncode != 0:
            print("the backup script failed:", r.stdout, r.stderr)
            return 1

        copies = sorted(out.glob("xellter-*.db"))
        if len(copies) != 1:
            print(f"expected one backup, got {copies}")
            return 1

        # The copy must open on its own and hold the rows: a snapshot of a live
        # database is only useful if it reads back.
        b = sqlite3.connect(f"file:{copies[0]}?mode=ro", uri=True)
        n = b.execute("SELECT COUNT(*) FROM requests").fetchone()[0]
        b.close()
        if n != 5:
            print(f"the backup holds {n} rows, want 5")
            return 1

        # The secrets and the config come along: a database without the clients
        # it logs for is not much use.
        if not list(out.glob("*-secrets.env")):
            print("the secrets file was not backed up")
            return 1
        if not list(out.glob("*-xellter.yaml")):
            print("the config was not backed up")
            return 1

        # The copies must not be readable by anyone else.
        for p in out.iterdir():
            if p.stat().st_mode & 0o077:
                print(f"{p.name} is readable by others: {oct(p.stat().st_mode)}")
                return 1

        # Only the last few copies are kept.
        for i in range(4):
            subprocess.run([sys.executable, str(SCRIPT), str(data), str(out), "3"],
                           capture_output=True, text=True)
        copies = sorted(out.glob("xellter-*.db"))
        if len(copies) > 3:
            print(f"the old copies were not pruned: {len(copies)} left")
            return 1

    print("backup ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
