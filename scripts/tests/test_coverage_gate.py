import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "coverage_gate.py"
SPEC = importlib.util.spec_from_file_location("coverage_gate", SCRIPT)
coverage_gate = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = coverage_gate
SPEC.loader.exec_module(coverage_gate)


def baseline_text(**overrides):
    value = {
        "version": 1,
        "target_percent": 90,
        "diff_percent": 90,
        "modules": {
            "daemon": {"covered": 1, "total": 2},
            "cli": {"covered": 1, "total": 2},
            "flutter": {"covered": 1, "total": 2},
        },
        "ignore": [],
    }
    value.update(overrides)
    return json.dumps(value)


class GoProfileTests(unittest.TestCase):
    def test_atomic_union_deduplicates_blocks_and_ors_hits(self):
        profile = """mode: atomic
github.com/heimdallm/daemon/internal/x.go:2.1,3.2 2 0
mode: atomic
github.com/heimdallm/daemon/internal/x.go:2.1,3.2 2 7
github.com/heimdallm/daemon/internal/x.go:5.1,5.9 1 0
"""
        report = coverage_gate.parse_go_profile_text(
            profile, coverage_gate.MODULES["daemon"]
        )
        self.assertEqual((2, 3), coverage_gate._filtered_counts(report, {}))
        self.assertEqual({"daemon/internal/x.go"}, set(report.files))

    def test_conflicting_duplicate_is_malformed(self):
        profile = """mode: atomic
github.com/heimdallm/daemon/x.go:1.1,1.2 1 0
github.com/heimdallm/daemon/x.go:1.1,1.2 2 0
"""
        with self.assertRaisesRegex(coverage_gate.GateError, "conflicting"):
            coverage_gate.parse_go_profile_text(profile, coverage_gate.MODULES["daemon"])

    def test_missing_empty_malformed_and_non_atomic_profiles_fail(self):
        bad_profiles = (
            "",
            "mode: atomic\n",
            "mode: set\ngithub.com/heimdallm/daemon/x.go:1.1,1.2 1 1\n",
            "mode: atomic\nnot a block\n",
        )
        for profile in bad_profiles:
            with self.subTest(profile=profile), self.assertRaises(coverage_gate.GateError):
                coverage_gate.parse_go_profile_text(
                    profile, coverage_gate.MODULES["daemon"]
                )

    def test_cli_import_prefix_maps_to_repo_path(self):
        report = coverage_gate.parse_go_profile_text(
            "mode: atomic\n"
            "github.com/theburrowhub/heimdallm/cli/internal/api/client.go:1.1,2.1 1 1\n",
            coverage_gate.MODULES["cli"],
        )
        self.assertEqual({"cli/internal/api/client.go"}, set(report.files))

    def test_zero_statement_control_flow_block_is_valid_but_weightless(self):
        report = coverage_gate.parse_go_profile_text(
            "mode: atomic\n"
            "github.com/heimdallm/daemon/a.go:1.2,1.2 0 0\n"
            "github.com/heimdallm/daemon/a.go:2.1,2.2 1 1\n",
            coverage_gate.MODULES["daemon"],
        )
        self.assertEqual((1, 1), coverage_gate._filtered_counts(report, {}))


class LcovTests(unittest.TestCase):
    def test_duplicate_records_are_unioned(self):
        report = coverage_gate.parse_lcov_text(
            """SF:lib/a.dart
DA:2,0
DA:4,1
LF:2
LH:1
end_of_record
SF:/checkout/flutter_app/lib/a.dart
DA:2,3
LF:1
LH:1
end_of_record
"""
        )
        self.assertEqual((2, 2), coverage_gate._filtered_counts(report, {}))

    def test_empty_and_malformed_reports_fail(self):
        reports = (
            "",
            "TN:\n",
            "SF:lib/a.dart\nDA:nope,1\nend_of_record\n",
            "SF:lib/a.dart\nDA:1,1\nLF:2\nLH:1\nend_of_record\n",
            "SF:lib/a.dart\nDA:1,1\n",
        )
        for report in reports:
            with self.subTest(report=report), self.assertRaises(coverage_gate.GateError):
                coverage_gate.parse_lcov_text(report)


class DiffTests(unittest.TestCase):
    def test_added_and_modified_lines_count_but_deletes_do_not(self):
        diff = """diff --git a/daemon/a.go b/daemon/a.go
--- a/daemon/a.go
+++ b/daemon/a.go
@@ -2 +2 @@
-old
+changed
@@ -7,2 +7,0 @@
-gone
-gone too
diff --git a/flutter_app/lib/new.dart b/flutter_app/lib/new.dart
--- /dev/null
+++ b/flutter_app/lib/new.dart
@@ -0,0 +1,2 @@
+one
+two
"""
        self.assertEqual(
            {
                "daemon/a.go": {2},
                "flutter_app/lib/new.dart": {1, 2},
            },
            coverage_gate.parse_unified_diff(diff),
        )

    def test_malformed_hunk_fails(self):
        with self.assertRaisesRegex(coverage_gate.GateError, "counts"):
            coverage_gate.parse_unified_diff(
                "+++ b/daemon/a.go\n@@ -1 +1 @@\n-old\n"
            )


class BaselineTests(unittest.TestCase):
    def test_ignore_requires_exact_path_reason_and_issue(self):
        parsed = coverage_gate.parse_baseline_text(
            baseline_text(
                ignore=[
                    {
                        "module": "flutter",
                        "path": "flutter_app/lib/a.g.dart",
                        "reason": "generated",
                        "issue": "#688",
                    }
                ]
            )
        )
        self.assertTrue(parsed.ignores[0].whole_file)
        for entry in (
            {"module": "flutter", "path": "flutter_app/lib/*.g.dart", "reason": "x", "issue": "#1"},
            {"module": "flutter", "path": "flutter_app/lib/a.dart", "reason": "", "issue": "#1"},
            {"module": "flutter", "path": "flutter_app/lib/a.dart", "reason": "x", "issue": "later"},
        ):
            with self.subTest(entry=entry), self.assertRaises(coverage_gate.GateError):
                coverage_gate.parse_baseline_text(baseline_text(ignore=[entry]))

    def test_empty_and_malformed_baselines_fail(self):
        for text in (
            "",
            "{",
            "[]",
            baseline_text(version=2),
            baseline_text(target_percent=89),
            baseline_text(diff_percent=89),
        ):
            with self.subTest(text=text), self.assertRaises(coverage_gate.GateError):
                coverage_gate.parse_baseline_text(text)


class EvaluationTests(unittest.TestCase):
    def setUp(self):
        self.baseline = coverage_gate.parse_baseline_text(baseline_text())
        self.reports = {
            "daemon": coverage_gate.parse_go_profile_text(
                "mode: atomic\ngithub.com/heimdallm/daemon/a.go:1.1,1.2 1 1\n"
                "github.com/heimdallm/daemon/a.go:2.1,2.2 1 0\n",
                coverage_gate.MODULES["daemon"],
            ),
            "cli": coverage_gate.parse_go_profile_text(
                "mode: atomic\ngithub.com/theburrowhub/heimdallm/cli/a.go:1.1,1.2 1 1\n"
                "github.com/theburrowhub/heimdallm/cli/a.go:2.1,2.2 1 0\n",
                coverage_gate.MODULES["cli"],
            ),
            "flutter": coverage_gate.parse_lcov_text(
                "SF:lib/a.dart\nDA:1,1\nDA:2,0\nLF:2\nLH:1\nend_of_record\n"
            ),
        }
        self.inventory = {
            "daemon/a.go",
            "cli/a.go",
            "flutter_app/lib/a.dart",
        }

    def test_exact_counts_and_covered_diff_pass(self):
        results, failures = coverage_gate.evaluate_gate(
            self.baseline,
            self.baseline,
            self.reports,
            self.inventory,
            {"daemon/a.go": {1}},
        )
        self.assertFalse(failures)
        self.assertEqual(3, len(results))

    def test_exact_baseline_mismatch_and_ratio_drop_fail(self):
        current = coverage_gate.parse_baseline_text(
            baseline_text(
                modules={
                    "daemon": {"covered": 1, "total": 3},
                    "cli": {"covered": 1, "total": 2},
                    "flutter": {"covered": 1, "total": 2},
                }
            )
        )
        _, failures = coverage_gate.evaluate_gate(
            current, self.baseline, self.reports, self.inventory, {}
        )
        self.assertTrue(any("report is 1/2" in failure for failure in failures))
        self.assertTrue(any("baseline ratio fell" in failure for failure in failures))

    def test_diff_below_ninety_fails(self):
        _, failures = coverage_gate.evaluate_gate(
            self.baseline,
            self.baseline,
            self.reports,
            self.inventory,
            {"daemon/a.go": {1, 2}},
        )
        self.assertTrue(any("diff coverage 1/2" in failure for failure in failures))

    def test_go_absent_unchanged_passes_but_changed_fails(self):
        inventory = self.inventory | {"daemon/declarations.go"}
        _, failures = coverage_gate.evaluate_gate(
            self.baseline, self.baseline, self.reports, inventory, {}
        )
        self.assertFalse(failures)
        _, failures = coverage_gate.evaluate_gate(
            self.baseline,
            self.baseline,
            self.reports,
            inventory,
            {"daemon/declarations.go": {1}},
        )
        self.assertTrue(any("declarations.go" in failure for failure in failures))

    def test_flutter_global_universe_is_fail_closed(self):
        inventory = self.inventory | {"flutter_app/lib/not_loaded.dart"}
        _, failures = coverage_gate.evaluate_gate(
            self.baseline, self.baseline, self.reports, inventory, {}
        )
        self.assertTrue(any("not_loaded.dart" in failure for failure in failures))

    def test_universe_exempt_unchanged_passes_but_changed_absent_fails(self):
        baseline = coverage_gate.parse_baseline_text(
            baseline_text(
                ignore=[
                    {
                        "module": "flutter",
                        "path": "flutter_app/lib/core/platform/platform_services_web.dart",
                        "reason": "collector limitation",
                        "issue": "#688",
                        "scope": "universe",
                    }
                ]
            )
        )
        exempt_path = "flutter_app/lib/core/platform/platform_services_web.dart"
        inventory = self.inventory | {exempt_path}
        _, failures = coverage_gate.evaluate_gate(
            baseline, baseline, self.reports, inventory, {}
        )
        self.assertFalse(failures)
        _, failures = coverage_gate.evaluate_gate(
            baseline,
            baseline,
            self.reports,
            inventory,
            {exempt_path: {1}},
        )
        self.assertTrue(any("platform_services_web.dart" in failure for failure in failures))

    def test_ignore_policy_cannot_be_added_or_widened(self):
        current = coverage_gate.parse_baseline_text(
            baseline_text(
                ignore=[
                    {
                        "module": "flutter",
                        "path": "flutter_app/lib/core/platform/platform_services_web.dart",
                        "reason": "collector limitation",
                        "issue": "#688",
                        "scope": "universe",
                    }
                ]
            )
        )
        _, failures = coverage_gate.evaluate_gate(
            current, self.baseline, self.reports, self.inventory, {}
        )
        self.assertTrue(any("ignore policy was added" in failure for failure in failures))

    def test_hard_floor_activates_at_target(self):
        baseline = coverage_gate.parse_baseline_text(
            baseline_text(
                modules={
                    "daemon": {"covered": 1, "total": 1},
                    "cli": {"covered": 1, "total": 2},
                    "flutter": {"covered": 1, "total": 2},
                }
            )
        )
        reports = dict(self.reports)
        reports["daemon"] = coverage_gate.parse_go_profile_text(
            "mode: atomic\ngithub.com/heimdallm/daemon/a.go:1.1,1.2 1 1\n",
            coverage_gate.MODULES["daemon"],
        )
        results, failures = coverage_gate.evaluate_gate(
            baseline, None, reports, self.inventory, {}
        )
        self.assertFalse(failures)
        self.assertTrue(results[0].hard_floor)


class DirectiveTests(unittest.TestCase):
    def test_unconfigured_directive_fails_and_configured_one_passes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "flutter_app/lib/a.dart"
            path.parent.mkdir(parents=True)
            path.write_text("// coverage:ignore\nvoid f() {}\n", encoding="utf-8")
            inventory = {"flutter_app/lib/a.dart"}
            plain = coverage_gate.parse_baseline_text(baseline_text())
            with self.assertRaisesRegex(coverage_gate.GateError, "no justified"):
                coverage_gate._validate_ignores_and_directives(root, plain, inventory)
            configured = coverage_gate.parse_baseline_text(
                baseline_text(
                    ignore=[
                        {
                            "module": "flutter",
                            "path": "flutter_app/lib/a.dart",
                            "reason": "unreachable adapter",
                            "issue": "#688",
                            "lines": [2],
                        }
                    ]
                )
            )
            coverage_gate._validate_ignores_and_directives(root, configured, inventory)

    def test_configured_line_ignore_needs_no_inline_directive(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "flutter_app/lib/a.dart"
            path.parent.mkdir(parents=True)
            path.write_text("void f() {}\n", encoding="utf-8")
            configured = coverage_gate.parse_baseline_text(
                baseline_text(
                    ignore=[
                        {
                            "module": "flutter",
                            "path": "flutter_app/lib/a.dart",
                            "reason": "instrumentation inconsistency",
                            "issue": "#688",
                            "lines": [1],
                        }
                    ]
                )
            )
            coverage_gate._validate_ignores_and_directives(
                root, configured, {"flutter_app/lib/a.dart"}
            )


if __name__ == "__main__":
    unittest.main()
