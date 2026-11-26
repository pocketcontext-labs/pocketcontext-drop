import hashlib
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
from probe_release import inspect


class ProbeReleaseTest(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.binary = Path(directory.name) / "guest"
        self.binary.write_bytes(b"known fixture")
        self.digest = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        self.op = Mock(side_effect=[{"attempt": "one"}, {}])

    def run_probe(self, probe):
        return inspect(self.binary, self.digest, self.op, probe)

    def test_cache_hit_skips_runner(self):
        self.op.side_effect = [{"cached": True, "state": "valid"}]
        probe = Mock()
        self.run_probe(probe)
        probe.assert_not_called()

    def test_cached_rejection_is_unsuccessful(self):
        self.op.side_effect = [{"cached": True, "state": "invalid"}]
        probe = Mock()
        with self.assertRaises(ValueError):
            self.run_probe(probe)
        probe.assert_not_called()

    def test_modified_binary_does_not_claim(self):
        self.binary.write_bytes(b"other")
        with self.assertRaises(ValueError):
            self.run_probe(Mock())
        self.op.assert_not_called()

    def test_worker_returns_receipt_with_claim_attempt(self):
        self.run_probe(Mock(return_value=subprocess.CompletedProcess([], 0, b'{"app":"crew"}')))
        self.op.assert_called_with("beta.complete", {
            "sha256": self.digest, "attempt": "one", "outcome": "success", "manifest": {"app": "crew"},
        })

    def test_incomplete_result_can_retry(self):
        with self.assertRaises(ValueError):
            self.run_probe(Mock(return_value=subprocess.CompletedProcess([], 0, b'{"app":')))
        self.assertEqual(self.op.call_args.args[1]["outcome"], "retry")

    def test_definitive_rejection_stays_rejected(self):
        with self.assertRaises(ValueError):
            self.run_probe(Mock(return_value=subprocess.CompletedProcess([], 3, b"")))
        self.assertEqual(self.op.call_args.args[1]["outcome"], "invalid")
