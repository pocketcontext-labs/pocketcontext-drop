"""Unit test of tests/leak_scan.py (python3 -m unittest discover -s tests -p 'test_*.py')."""
import base64
from pathlib import Path
import sqlite3
import tempfile
import unittest

import leak_scan

SECRET = 'synthetic-leak-sentinel-0001'


class LeakScanTest(unittest.TestCase):
    def test_finds_every_form_and_names_no_value(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'nested').mkdir()
            (root / 'clean.log').write_text('nothing here')
            (root / 'nested' / 'state.json').write_text('{"cache":"%s"}' % base64.b64encode(SECRET.encode()).decode())
            (root / 'store').mkdir()
            (root / 'store' / '1').write_text(SECRET)
            with sqlite3.connect(root / 'data.db') as conn:
                conn.execute('CREATE TABLE t (v TEXT)')
                conn.execute('INSERT INTO t VALUES (?)', (SECRET.encode().hex(),))
            conn.close()
            findings = leak_scan.scan({'secret': SECRET, 'other': 'never-present'}, paths=[root],
                                      texts={'answer': 'ok', 'mcp': b'x' + SECRET.encode()}, exclude=[root / 'store'])
            sources = sorted(Path(s).name if '/' in s else s for s, _ in findings)
            self.assertEqual(sources, ['data.db', 'data.db (rows)', 'mcp', 'state.json'])
            self.assertTrue(all(label == 'secret' for _, label in findings))
            with self.assertRaises(AssertionError) as raised:
                leak_scan.require_clean({'secret': SECRET}, paths=[root])
            self.assertNotIn(SECRET, str(raised.exception))
            with self.assertRaises(FileNotFoundError):
                leak_scan.scan({'secret': SECRET}, paths=[root / 'missing'])
            with self.assertRaises(ValueError):
                leak_scan.scan({'short': 'abc'})


if __name__ == '__main__':
    unittest.main()
