"""Secret-leak scan for the Python end-to-end tests (stdlib only).

The same scan as internal/leakscan for Go tests: give it sentinels (synthetic
secret values with a label) and sources, and it reports every sentinel found,
naming the source and the label, never the value. Each value is also looked
for as standard and URL base64 and as hex.

Sources:
  - paths: files, or directories walked recursively. SQLite databases are read
    row by row (every value of every table) and also as raw bytes, which
    catches freed pages and WAL files; every other file as raw bytes.
  - texts: a mapping of name to str or bytes (HTTP answers, MCP output, CLI
    output, captured logs).
  - exclude: paths whose contents legitimately hold a value (the fake secret
    store) and are skipped.

The node agent's state file (M9): tests/drop_e2e.py adds its state directory
(or state.json) to the paths of its require_clean call, for example

    leak_scan.require_clean(sentinels, paths=[state_dir, desired, reported, agent_log, db, server_log])

Run directly to scan from the command line (values on stdin, one per line):

    python3 tests/leak_scan.py PATH... < values
"""
import base64
from pathlib import Path
import sqlite3
import sys

SQLITE_HEADER = b'SQLite format 3\x00'


def _variants(value):
    raw = value.encode() if isinstance(value, str) else value
    return [raw, base64.b64encode(raw), base64.urlsafe_b64encode(raw).rstrip(b'='), raw.hex().encode()]


def _sqlite_rows(path):
    """Every value of every table of a SQLite file, as bytes; empty if it cannot be read."""
    out = []
    try:
        conn = sqlite3.connect(f'file:{path}?mode=ro', uri=True)
    except sqlite3.Error:
        return b''
    try:
        tables = [row[0] for row in conn.execute("SELECT name FROM sqlite_master WHERE type = 'table'")]
        for table in tables:
            quoted = table.replace('"', '""')
            for row in conn.execute(f'SELECT * FROM "{quoted}"'):
                for value in row:
                    out.append(value if isinstance(value, bytes) else str(value).encode())
    except sqlite3.Error:
        pass
    finally:
        conn.close()
    return b'\n'.join(out)


def _files(path, exclude):
    path = Path(path)
    if not path.exists():
        raise FileNotFoundError(f'leak scan source does not exist: {path}')
    candidates = [path] if path.is_file() else sorted(p for p in path.rglob('*') if p.is_file())
    for candidate in candidates:
        if not any(candidate == e or e in candidate.parents for e in exclude):
            yield candidate


def scan(sentinels, paths=(), texts=None, exclude=()):
    """Return [(source, label)] for every sentinel found. sentinels maps label to value."""
    for label, value in sentinels.items():
        if len(value) < 8:
            raise ValueError(f'sentinel {label} is shorter than 8 characters')
    forms = {label: _variants(value) for label, value in sentinels.items()}
    exclude = [Path(e) for e in exclude]
    findings = []

    def check(source, data):
        for label, variants in forms.items():
            if any(v in data for v in variants):
                findings.append((source, label))

    for path in paths:
        for file in _files(path, exclude):
            data = file.read_bytes()
            check(str(file), data)
            if data.startswith(SQLITE_HEADER):
                check(f'{file} (rows)', _sqlite_rows(file))
    for name, text in (texts or {}).items():
        check(name, text.encode() if isinstance(text, str) else text)
    return findings


def require_clean(sentinels, paths=(), texts=None, exclude=()):
    """Raise AssertionError naming every leak (source and label, never the value)."""
    findings = scan(sentinels, paths, texts, exclude)
    if findings:
        raise AssertionError('secret leak: ' + '; '.join(f'{label} in {source}' for source, label in findings))


if __name__ == '__main__':
    values = [line.rstrip('\n') for line in sys.stdin if line.strip()]
    found = scan({f'value {i + 1}': v for i, v in enumerate(values)}, sys.argv[1:])
    for source, label in found:
        print(f'{label} in {source}')
    sys.exit(1 if found else 0)
