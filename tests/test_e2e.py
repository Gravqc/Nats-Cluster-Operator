"""State transitions for the shared Job wait."""

import subprocess
import unittest
from unittest.mock import patch

import e2e


class ProbeJob(unittest.TestCase):
    @patch.object(e2e, "kubectl")
    @patch.object(e2e, "get")
    @patch.object(e2e.time, "sleep")
    def test_failed_job_reports_reason_and_logs_without_waiting(
        self, sleep, get, kubectl
    ):
        get.return_value = {
            "status": {
                "conditions": [
                    {
                        "type": "Failed",
                        "status": "True",
                        "reason": "BackoffLimitExceeded",
                    }
                ]
            }
        }
        kubectl.return_value = subprocess.CompletedProcess([], 0, "probe error", "")
        with self.assertRaisesRegex(
            RuntimeError, r"(?s)Job demo failed.*BackoffLimitExceeded.*probe error"
        ):
            e2e.job("demo")
        sleep.assert_not_called()

    @patch.object(e2e, "REPORT", {})
    @patch.object(e2e, "kubectl")
    @patch.object(e2e, "get")
    @patch.object(e2e.time, "sleep")
    def test_pending_job_waits_then_records_success(self, sleep, get, kubectl):
        get.side_effect = [
            {},
            {"status": {"conditions": [{"type": "Complete", "status": "True"}]}},
        ]
        kubectl.return_value = subprocess.CompletedProcess([], 0, '{"verified":1}', "")
        e2e.job("demo")
        sleep.assert_called_once()
        self.assertEqual(e2e.REPORT["probeResults"]["demo"], {"verified": 1})
