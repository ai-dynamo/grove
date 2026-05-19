#!/usr/bin/env python3
# /*
# Copyright 2026 The Grove Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
# */
"""
E2E test matrix selector for GitHub Actions.

Computes which (suite × backend) rows the e2e job should run on a given
PR (mode=pr) or scheduled run (mode=nightly), based on changed file paths
and PR labels.

The selector emits the matrix as JSON in the GHA ``{"include": [...]}``
shape. Two outputs are emitted in a single call:

  - ``run``: rows the matrix should actually execute
  - ``skip``: rows the e2e-skip mirror should emit as synthetic passes,
    so that branch-protection-required check names stay stable across PRs

The union of ``run`` + ``skip`` always equals the full matrix
(``ALL_ROWS``); the two are disjoint.

Selection logic
---------------
- ``mode=nightly``: ``run = ALL_ROWS``, ``skip = []``. Path filter and
  labels are ignored.
- ``mode=pr`` with ``--has-label run-e2e``: same as nightly. This is the
  "safety escape" — a reviewer can force the full matrix without having
  to figure out which path triggers which rows.
- ``mode=pr`` with ``--draft`` and no ``run-e2e`` label: ``run = []``,
  ``skip = ALL_ROWS``. Draft PRs do not gate merges, so we emit the full
  set as synthetic passes; the contributor can add the label to force
  real runs.
- ``mode=pr`` otherwise: changed files are matched against ``PATH_RULES``
  in order; the union of matched "affected" sets selects which rows run.
  Unselected rows go to ``skip``.

Adding a new backend
--------------------
1. Add the per-backend rows to ``ALL_ROWS`` (test_name must be unique).
2. If the backend has its own scheduler package subdir
   (``operator/internal/scheduler/<name>/``), add a path rule above the
   generic ``scheduler/**`` shared-framework rule.
3. Add testdata samples covering the new backend's path-filter case and
   re-run the unit tests.
"""

import argparse
import fnmatch
import json
import sys
from typing import Any

# ---------------------------------------------------------------------------
# Matrix definition. Keep test_name unique across rows.
# ---------------------------------------------------------------------------
ALL_ROWS: list[dict[str, Any]] = [
    # ---- kai-scheduler (primary backend) ----
    {"test_name": "gang_scheduling", "test_pattern": "^Test_GS",
     "backend": "kai-scheduler", "create_flags": "",
     "make_target": "run-e2e-full", "tier": "capability"},
    {"test_name": "rolling_updates", "test_pattern": "^Test_RU",
     "backend": "kai-scheduler", "create_flags": "",
     "make_target": "run-e2e-full", "tier": "sensitive"},
    {"test_name": "ondelete_updates", "test_pattern": "^Test_OD",
     "backend": "kai-scheduler", "create_flags": "",
     "make_target": "run-e2e-full", "tier": "sensitive"},
    {"test_name": "startup_ordering", "test_pattern": "^Test_SO",
     "backend": "kai-scheduler", "create_flags": "",
     "make_target": "run-e2e-real-full", "tier": "sensitive"},
    {"test_name": "Topology_Aware_Scheduling", "test_pattern": "^Test_TAS",
     "backend": "kai-scheduler", "create_flags": "",
     "make_target": "run-e2e-full", "tier": "capability"},
    {"test_name": "cert_management", "test_pattern": "^Test_CM",
     "backend": "kai-scheduler", "create_flags": "",
     "make_target": "run-e2e-full", "tier": "agnostic"},
    {"test_name": "auto_mnnvl", "test_pattern": "^Test_AutoMNNVL",
     "backend": "kai-scheduler", "create_flags": "",
     "make_target": "run-e2e-mnnvl-full", "tier": "capability"},
    {"test_name": "crd_installer", "test_pattern": "^Test_CRD_Installer",
     "backend": "kai-scheduler", "create_flags": "",
     "make_target": "run-e2e-full", "tier": "agnostic"},
    {"test_name": "resource_sharing", "test_pattern": "^Test_RS",
     "backend": "kai-scheduler", "create_flags": "",
     "make_target": "run-e2e-full", "tier": "capability"},
    # ---- default-scheduler ----
    {"test_name": "rolling_updates_default-scheduler", "test_pattern": "^Test_RU",
     "backend": "default-scheduler",
     "create_flags": "-f hack/e2e-default-scheduler.yaml",
     "make_target": "run-e2e-full", "tier": "sensitive"},
    {"test_name": "ondelete_updates_default-scheduler", "test_pattern": "^Test_OD",
     "backend": "default-scheduler",
     "create_flags": "-f hack/e2e-default-scheduler.yaml",
     "make_target": "run-e2e-full", "tier": "sensitive"},
    {"test_name": "startup_ordering_default-scheduler", "test_pattern": "^Test_SO",
     "backend": "default-scheduler",
     "create_flags": "-f hack/e2e-default-scheduler.yaml",
     "make_target": "run-e2e-real-full", "tier": "sensitive"},
]

# ---------------------------------------------------------------------------
# Path filter rules. Order matters: first matching rule per file wins.
# Each rule maps a glob set to an "affected" set; "all" means all backends,
# "agnostic" means include agnostic-tier rows even when no specific backend
# matched. The selector unions affected sets across all changed files.
# ---------------------------------------------------------------------------
PATH_RULES: list[dict[str, Any]] = [
    # Docs / pure markdown / top-level metadata: never trigger e2e.
    {"globs": ["docs/**", "*.md", "**/*.md",
               "ATTRIBUTION.md", "LICENSE", "OWNERS",
               "MAINTAINERS.md", "code-of-conduct.md",
               "SECURITY.md", "CONTRIBUTING.md"],
     "affected": set()},
    # Backend-specific subpaths under the scheduler package.
    {"globs": ["operator/internal/scheduler/kai/**"],
     "affected": {"kai-scheduler"}},
    {"globs": ["operator/internal/scheduler/kube/**"],
     "affected": {"default-scheduler"}},
    # Shared scheduler framework (anything else under scheduler/).
    {"globs": ["operator/internal/scheduler/**"],
     "affected": {"all"}},
    # API surface and Helm charts: broad — affects every backend's deploy.
    {"globs": ["operator/api/**", "operator/charts/**"],
     "affected": {"all", "agnostic"}},
    # E2E infra, CI workflows, hack scripts: broad.
    {"globs": ["operator/e2e/**", "operator/hack/**",
               ".github/**", "hack/**"],
     "affected": {"all", "agnostic"}},
    # Fallback for anything else under operator/: treat as broad change.
    {"globs": ["operator/**"],
     "affected": {"all"}},
]


def _match(path: str, globs: list[str]) -> bool:
    return any(fnmatch.fnmatch(path, g) for g in globs)


def compute_affected(changed_files: list[str]) -> set[str]:
    """Walk PATH_RULES; first match per file contributes to the affected set."""
    affected: set[str] = set()
    for path in changed_files:
        for rule in PATH_RULES:
            if _match(path, rule["globs"]):
                affected |= rule["affected"]
                break
        # No-match files (e.g. unknown top-level paths) are ignored intentionally.
    return affected


def select_rows(affected: set[str]) -> list[dict[str, Any]]:
    """Filter ALL_ROWS by an affected set.

    'all' matches every row; otherwise a row matches if its backend is in the
    set, or if 'agnostic' is in the set and the row is agnostic-tier.
    """
    if not affected:
        return []
    if "all" in affected:
        return list(ALL_ROWS)
    selected: list[dict[str, Any]] = []
    include_agnostic = "agnostic" in affected
    for row in ALL_ROWS:
        if row["backend"] in affected:
            selected.append(row)
        elif include_agnostic and row["tier"] == "agnostic":
            selected.append(row)
    return selected


def split(rows_run: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Return (run, skip) where skip = ALL_ROWS - run, preserving order."""
    run_names = {r["test_name"] for r in rows_run}
    skip = [r for r in ALL_ROWS if r["test_name"] not in run_names]
    return rows_run, skip


def _strip_internal(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Drop selector-internal fields before emitting JSON."""
    out = []
    for r in rows:
        out.append({k: v for k, v in r.items() if k != "tier"})
    return out


def compute(mode: str, changed_files: list[str], labels: list[str],
            draft: bool) -> dict[str, Any]:
    """Top-level decision tree. Returns dict with 'run', 'skip', 'has_run',
    'has_skip', 'reason' (a short string for logging)."""
    has_run_e2e_label = "run-e2e" in labels

    if mode == "nightly":
        run = list(ALL_ROWS)
        reason = "nightly: full matrix"
    elif has_run_e2e_label:
        run = list(ALL_ROWS)
        reason = "pr+run-e2e label: full matrix (safety escape)"
    elif draft:
        # Draft PR with no label: do not run e2e; all rows go to skip mirror.
        run = []
        reason = "pr+draft+no run-e2e label: skip all"
    else:
        affected = compute_affected(changed_files)
        run = select_rows(affected)
        if not run:
            reason = f"pr: no rows affected (affected={sorted(affected) or '∅'})"
        else:
            reason = f"pr: affected={sorted(affected)}"

    run, skip = split(run)
    return {
        "run": {"include": _strip_internal(run)},
        "skip": {"include": _strip_internal(skip)},
        "has_run": len(run) > 0,
        "has_skip": len(skip) > 0,
        "reason": reason,
    }


def _read_changed_files(arg: str) -> list[str]:
    if arg == "-":
        lines = sys.stdin.read().splitlines()
    else:
        with open(arg) as f:
            lines = f.read().splitlines()
    return [line.strip() for line in lines if line.strip()]


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--mode", required=True, choices=["pr", "nightly"])
    ap.add_argument("--changed-files", default="-",
                    help="path to file containing one changed path per line, "
                         "or '-' for stdin. ignored for mode=nightly.")
    ap.add_argument("--labels", default="",
                    help="comma-separated list of PR labels. "
                         "'run-e2e' triggers full matrix in pr mode.")
    ap.add_argument("--draft", action="store_true",
                    help="set if the PR is in draft state.")
    ap.add_argument("--show", default="all",
                    choices=["all", "run", "skip", "has_run", "has_skip", "reason"],
                    help="which part of the result to print "
                         "(default: full JSON object).")
    args = ap.parse_args(argv)

    if args.mode == "nightly":
        changed_files: list[str] = []
    else:
        changed_files = _read_changed_files(args.changed_files)

    labels = [s.strip() for s in args.labels.split(",") if s.strip()]
    result = compute(args.mode, changed_files, labels, args.draft)

    if args.show == "all":
        print(json.dumps(result, indent=2))
    elif args.show in ("run", "skip"):
        print(json.dumps(result[args.show], separators=(",", ":")))
    else:
        print(result[args.show])
    return 0


if __name__ == "__main__":
    sys.exit(main())
