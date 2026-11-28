"""Regression checks for the lagging-replica experiment verdict."""

import unittest

from catchup_comparison import decide, longest_gap, ready_with_lag


class LagMeasurement(unittest.TestCase):
    def setUp(self):
        self.chart = {
            "first_restart": {
                "readyWithLagBefore": True,
                "publisherPodStable": True,
                "longestOverlappingGapSeconds": 6,
            },
            "control_max_gap_seconds_bound": 1,
            "integrity": True,
        }
        self.operator = {
            "blocked_with_ready_lag_samples": 3,
            "restarts_during_gated_window": 0,
            "restarts_while_replica_not_current": 0,
            "gated_max_gap_seconds": 0,
            "control_max_gap_seconds_bound": 1,
            "integrity": True,
        }

    def test_gate_needs_a_ready_but_lagging_replica_and_no_restart(self):
        self.assertTrue(decide(self.chart, self.operator)["supportsGate"])
        for field, value in (
            ("blocked_with_ready_lag_samples", 0),
            ("restarts_during_gated_window", 1),
            ("restarts_while_replica_not_current", 1),
        ):
            with self.subTest(field=field):
                operator = {**self.operator, field: value}
                self.assertFalse(decide(self.chart, operator)["supportsGate"])

    def test_chart_gap_needs_ready_lag_before_first_restart(self):
        self.chart["first_restart"]["readyWithLagBefore"] = False
        self.assertFalse(decide(self.chart, self.operator)["supportsGate"])

    def test_ready_lag_excludes_unready_pods_and_offline_or_missing_replicas(self):
        replica = {"current": False, "offline": False, "lag": 42}
        observation = {
            "pods": {
                name: {"ready": True, "terminating": False} for name in ("a", "b", "c")
            },
            "stream": {"replicas": {"b": replica}},
        }
        self.assertTrue(ready_with_lag(observation, "b"))
        self.assertFalse(ready_with_lag(observation, "missing"))
        replica["offline"] = True
        self.assertFalse(ready_with_lag(observation, "b"))
        replica["offline"] = False
        observation["pods"]["c"]["ready"] = False
        self.assertFalse(ready_with_lag(observation, "b"))

    def test_gap_that_crosses_a_window_boundary_counts_inside_both(self):
        gaps = [{"start": 28, "end": 34}]
        self.assertEqual(longest_gap(gaps, 0, 30), 2)
        self.assertEqual(longest_gap(gaps, 30, 120), 4)
        self.assertEqual(longest_gap(gaps, 120, 150), 0)
