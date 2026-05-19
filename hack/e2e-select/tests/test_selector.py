# /*
# Copyright 2026 The Grove Authors.
# SPDX-License-Identifier: Apache-2.0
# */
"""Unit tests for the e2e matrix selector.

Run from repo root:

    python3 -m pytest hack/e2e-select/tests/ -v

or without pytest:

    python3 hack/e2e-select/tests/test_selector.py
"""

import json
import os
import sys
import unittest
from pathlib import Path

# Make the selector importable when invoked from the repo root.
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))

import main as selector  # noqa: E402


REPO_ROOT = HERE.parent.parent.parent
TESTDATA = HERE.parent / "testdata"


def _names(rows: list[dict]) -> list[str]:
    return [r["test_name"] for r in rows]


def _backends(rows: list[dict]) -> set[str]:
    return {r["backend"] for r in rows}


class TestModes(unittest.TestCase):
    """Top-level mode selection."""

    def test_nightly_runs_full_matrix(self):
        r = selector.compute("nightly", changed_files=[], labels=[], draft=False)
        self.assertEqual(len(r["run"]["include"]), len(selector.ALL_ROWS))
        self.assertEqual(r["skip"]["include"], [])
        self.assertTrue(r["has_run"])
        self.assertFalse(r["has_skip"])

    def test_nightly_ignores_changed_files_and_labels(self):
        r = selector.compute("nightly",
                             changed_files=["docs/foo.md"],
                             labels=["run-e2e"],
                             draft=True)
        self.assertEqual(len(r["run"]["include"]), len(selector.ALL_ROWS))

    def test_pr_run_e2e_label_forces_full_matrix(self):
        # Even with a docs-only change, run-e2e label overrides.
        r = selector.compute("pr",
                             changed_files=["docs/foo.md"],
                             labels=["run-e2e"],
                             draft=False)
        self.assertEqual(len(r["run"]["include"]), len(selector.ALL_ROWS))
        self.assertEqual(r["skip"]["include"], [])

    def test_pr_draft_no_label_emits_no_run(self):
        r = selector.compute("pr",
                             changed_files=["operator/internal/scheduler/kai/foo.go"],
                             labels=[],
                             draft=True)
        self.assertEqual(r["run"]["include"], [])
        self.assertEqual(len(r["skip"]["include"]), len(selector.ALL_ROWS))


class TestPathFilters(unittest.TestCase):
    """Path-rule logic for mode=pr (non-draft, no label)."""

    def _run(self, files):
        return selector.compute("pr", changed_files=files,
                                labels=[], draft=False)

    def test_docs_only_runs_nothing(self):
        r = self._run(["docs/foo.md", "README.md"])
        self.assertEqual(r["run"]["include"], [])
        self.assertEqual(len(r["skip"]["include"]), len(selector.ALL_ROWS))

    def test_kai_subpath_runs_only_kai_rows(self):
        r = self._run(["operator/internal/scheduler/kai/backend.go"])
        backends = _backends(r["run"]["include"])
        self.assertEqual(backends, {"kai-scheduler"})
        # Ensure default-scheduler rows landed in skip.
        skip_backends = _backends(r["skip"]["include"])
        self.assertIn("default-scheduler", skip_backends)

    def test_kube_subpath_runs_only_default_scheduler(self):
        r = self._run(["operator/internal/scheduler/kube/backend.go"])
        backends = _backends(r["run"]["include"])
        self.assertEqual(backends, {"default-scheduler"})

    def test_shared_scheduler_runs_all(self):
        # File under scheduler/ but NOT under kai/ or kube/ → all backends.
        r = self._run(["operator/internal/scheduler/types.go"])
        self.assertEqual(len(r["run"]["include"]), len(selector.ALL_ROWS))

    def test_charts_runs_all_plus_agnostic(self):
        r = self._run(["operator/charts/values.yaml"])
        self.assertEqual(len(r["run"]["include"]), len(selector.ALL_ROWS))

    def test_e2e_infra_runs_all(self):
        r = self._run(["operator/e2e/tests/foo_test.go"])
        self.assertEqual(len(r["run"]["include"]), len(selector.ALL_ROWS))

    def test_workflow_change_runs_all(self):
        r = self._run([".github/workflows/build-check-test.yaml"])
        self.assertEqual(len(r["run"]["include"]), len(selector.ALL_ROWS))

    def test_mixed_kai_plus_docs_keeps_only_kai(self):
        r = self._run(["operator/internal/scheduler/kai/x.go",
                       "docs/proposals/foo.md"])
        backends = _backends(r["run"]["include"])
        self.assertEqual(backends, {"kai-scheduler"})

    def test_mixed_kai_plus_kube_runs_both(self):
        r = self._run(["operator/internal/scheduler/kai/x.go",
                       "operator/internal/scheduler/kube/y.go"])
        backends = _backends(r["run"]["include"])
        self.assertEqual(backends, {"kai-scheduler", "default-scheduler"})

    def test_empty_changed_files_emits_no_run(self):
        r = self._run([])
        self.assertEqual(r["run"]["include"], [])
        self.assertEqual(len(r["skip"]["include"]), len(selector.ALL_ROWS))

    def test_unknown_top_level_path_is_ignored(self):
        # Some random top-level file no rule matches → contributes nothing.
        r = self._run(["unrelated-top-level-file.txt"])
        self.assertEqual(r["run"]["include"], [])

    def test_fallback_rule_for_other_operator_paths(self):
        # operator/scheduler.go (hypothetical) — not under api/charts/e2e/
        # but under operator/ → fallback rule fires.
        r = self._run(["operator/some-toplevel-go-file.go"])
        self.assertEqual(len(r["run"]["include"]), len(selector.ALL_ROWS))


class TestSplitInvariants(unittest.TestCase):
    """Invariants of the split() helper."""

    def test_run_plus_skip_equals_all(self):
        for files in (
            [],
            ["operator/internal/scheduler/kai/x.go"],
            ["operator/internal/scheduler/kube/x.go"],
            ["operator/internal/scheduler/types.go"],
            ["docs/foo.md"],
            ["operator/charts/values.yaml"],
        ):
            r = selector.compute("pr", files, labels=[], draft=False)
            run = _names(r["run"]["include"])
            skip = _names(r["skip"]["include"])
            self.assertEqual(set(run) | set(skip),
                             {row["test_name"] for row in selector.ALL_ROWS},
                             msg=f"files={files}")
            self.assertEqual(set(run) & set(skip), set(),
                             msg=f"run/skip not disjoint for files={files}")

    def test_emitted_rows_have_no_tier_field(self):
        r = selector.compute("nightly", [], [], draft=False)
        for row in r["run"]["include"]:
            self.assertNotIn("tier", row,
                             msg="tier is selector-internal, must not leak to GHA")


class TestRowsConsistency(unittest.TestCase):
    """Sanity checks on ALL_ROWS itself."""

    def test_test_names_are_unique(self):
        names = [r["test_name"] for r in selector.ALL_ROWS]
        self.assertEqual(len(names), len(set(names)))

    def test_required_fields_present(self):
        required = {"test_name", "test_pattern", "backend",
                    "create_flags", "make_target", "tier"}
        for row in selector.ALL_ROWS:
            self.assertEqual(set(row.keys()) & required, required,
                             msg=f"missing fields in row: {row}")

    def test_only_known_tiers(self):
        for row in selector.ALL_ROWS:
            self.assertIn(row["tier"],
                          {"agnostic", "sensitive", "capability"},
                          msg=f"unknown tier in row: {row}")


class TestGoldenSamples(unittest.TestCase):
    """Regenerate samples in testdata/ and assert they match committed files.

    Run with E2E_SELECT_REGENERATE=1 to update the golden files instead.
    """

    SAMPLES = {
        "pr-mode-kai-only.json": dict(
            mode="pr",
            changed_files=["operator/internal/scheduler/kai/backend.go"],
            labels=[], draft=False),
        "pr-mode-kube-only.json": dict(
            mode="pr",
            changed_files=["operator/internal/scheduler/kube/backend.go"],
            labels=[], draft=False),
        "pr-mode-shared-scheduler.json": dict(
            mode="pr",
            changed_files=["operator/internal/scheduler/types.go"],
            labels=[], draft=False),
        "pr-mode-docs-only.json": dict(
            mode="pr",
            changed_files=["docs/proposals/foo.md", "README.md"],
            labels=[], draft=False),
        "pr-mode-run-e2e-label.json": dict(
            mode="pr",
            changed_files=["docs/proposals/foo.md"],
            labels=["run-e2e"], draft=False),
        "pr-mode-draft-no-label.json": dict(
            mode="pr",
            changed_files=["operator/internal/scheduler/kai/backend.go"],
            labels=[], draft=True),
        "nightly-mode-full.json": dict(
            mode="nightly",
            changed_files=[], labels=[], draft=False),
    }

    def test_golden_samples_match(self):
        regenerate = os.environ.get("E2E_SELECT_REGENERATE") == "1"
        for fname, kwargs in self.SAMPLES.items():
            with self.subTest(sample=fname):
                got = selector.compute(**kwargs)
                # Drop 'reason' from golden files — it's diagnostic, not contract.
                got_stripped = {k: v for k, v in got.items() if k != "reason"}
                path = TESTDATA / fname
                if regenerate:
                    path.write_text(json.dumps(got_stripped, indent=2) + "\n")
                    continue
                self.assertTrue(path.exists(),
                                f"missing golden file {path}; "
                                f"run with E2E_SELECT_REGENERATE=1 to create")
                expected = json.loads(path.read_text())
                self.assertEqual(got_stripped, expected,
                                 f"selector output diverged from {fname}")


if __name__ == "__main__":
    unittest.main(verbosity=2)
