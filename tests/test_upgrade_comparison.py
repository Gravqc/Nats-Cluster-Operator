"""Regression checks for the experiment's recovery measurement."""

import unittest

from upgrade_comparison import recovery_state


class RecoveryMeasurement(unittest.TestCase):
    def test_cached_ready_does_not_prove_process_replacement(self):
        status = [{"name": "nats", "containerID": "containerd://old", "ready": True}]
        self.assertEqual(
            recovery_state({"leader": "a"}, status, "old", "a"), (False, False)
        )

    def test_same_server_can_be_leader_after_verified_restart(self):
        status = [
            {
                "name": "nats",
                "containerID": "containerd://new",
                "ready": True,
                "lastState": {
                    "terminated": {"containerID": "containerd://old", "exitCode": 137}
                },
            }
        ]
        self.assertEqual(
            recovery_state({"leader": "a"}, status, "old", "a"), (True, True)
        )
        self.assertEqual(
            recovery_state({"leader": None}, status, "old", "a"), (False, True)
        )
        status[0]["ready"] = False
        self.assertEqual(
            recovery_state({"leader": "b"}, status, "old", "a"), (True, False)
        )

    def test_different_leader_does_not_prove_original_process_died(self):
        status = [{"name": "nats", "containerID": "containerd://old", "ready": True}]
        self.assertEqual(
            recovery_state({"leader": "b"}, status, "old", "a"), (True, False)
        )


if __name__ == "__main__":
    unittest.main()
