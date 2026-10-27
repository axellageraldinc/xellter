#!/usr/bin/env python3
"""Back up the xellter state.

The request log and the leaderboard scores live in a SQLite database that runs
in WAL mode, so copying the file while the service is writing is not safe.
This uses SQLite's online backup API, which takes a consistent snapshot of a
live database.

The copies live OUTSIDE the data directory on purpose: the data directory is
the thing that gets lost or wiped, so a backup inside it is no backup at all.
The config file is copied too, because a database without the clients it logs
for is not much use.

Usage: xellter-backup.py <data-dir> [backup-dir] [keep]
"""
import os
import shutil
import sqlite3
import sys
import time
from pathlib import Path


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    data = Path(sys.argv[1]).resolve()
    out = Path(sys.argv[2]).resolve() if len(sys.argv) > 2 else data.parent / "xellter-backups"
    keep = int(sys.argv[3]) if len(sys.argv) > 3 else 7
    db = data / "xellter.db"
    if not db.is_file():
        print(f"no database at {db}", file=sys.stderr)
        return 1

    out.mkdir(parents=True, exist_ok=True)
    os.chmod(out, 0o700)

    stamp = time.strftime("%Y%m%d-%H%M%S")
    target = out / f"xellter-{stamp}.db"

    # The online backup API: a consistent snapshot of a database that is being
    # written to right now. Never copy the file itself for that.
    src = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    dst = sqlite3.connect(target)
    try:
        src.backup(dst)
    finally:
        dst.close()
        src.close()
    os.chmod(target, 0o600)

    # The config names the clients the log belongs to. The secrets file holds
    # the kenari keys; it is copied so a restore needs nothing else.
    for name in ("secrets.env",):
        p = data / name
        if p.is_file():
            shutil.copy2(p, out / f"{stamp}-{name}")
            os.chmod(out / f"{stamp}-{name}", 0o600)
    cfg = data.parent / "xellter.yaml"
    if cfg.is_file():
        shutil.copy2(cfg, out / f"{stamp}-xellter.yaml")
        os.chmod(out / f"{stamp}-xellter.yaml", 0o600)

    # Keep the last few copies.
    copies = sorted(out.glob("xellter-*.db"))
    for old in copies[:-keep] if keep > 0 else copies:
        old.unlink(missing_ok=True)
        for extra in out.glob(old.name.replace(".db", "-*")):
            extra.unlink(missing_ok=True)

    print(f"backup written to {target} ({target.stat().st_size} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
