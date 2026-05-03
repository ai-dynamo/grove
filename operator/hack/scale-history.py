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

"""Store Grove scale-test results and dashboard files.

Examples:
  Local testing:
    python operator/hack/scale-history.py local \
      --input operator/e2e/tests/scale/ScaleTest_20 \
      --history-dir /tmp/grove-scale-history

  History branch:
    python operator/hack/scale-history.py branch \
      --input "$RUNNER_TEMP/grove-scale" \
      --commit "$GITHUB_SHA" \
      --run-url "$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID"
"""

from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


RESULT_FILE = "scale-test-results.json"
DEFAULT_HISTORY_DIR = Path("/tmp/grove-scale-history")
DEFAULT_BRANCH_WORKDIR = Path("/tmp/grove-scale-history-branch")
DEFAULT_BRANCH = "scale-test-history"
SCRIPT_DIR = Path(__file__).resolve().parent
DASHBOARD_DIR = SCRIPT_DIR / "scale-dashboard"
RUN_ID_RE = re.compile(r"^run-(\d{8})-(\d{6})$")


def main() -> int:
    args = parse_args()
    result_path = find_result_file(Path(args.input))
    result = read_json(result_path)
    commit = args.commit or best_effort_git(["rev-parse", "HEAD"])
    run_url = args.run_url or ""

    metrics = flatten_metrics(result)

    if args.command == "local":
        changed = append_history(
            history_dir=Path(args.history_dir),
            result_path=result_path,
            result=result,
            commit=commit,
            run_url=run_url,
            metrics=metrics,
        )
        copy_dashboard_files(Path(args.history_dir))
        return 0 if changed is not None else 1

    history_dir = prepare_branch_workdir(
        workdir=Path(args.workdir),
        branch=args.branch,
        repo=args.repo,
    )
    changed = append_history(
        history_dir=history_dir,
        result_path=result_path,
        result=result,
        commit=commit,
        run_url=run_url,
        metrics=metrics,
    )
    copy_dashboard_files(history_dir)
    commit_and_push(history_dir, args.branch, result, changed)
    return 0 if changed is not None else 1


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Store a Grove scale-test result and dashboard in history."
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    def add_common(subparser: argparse.ArgumentParser) -> None:
        subparser.add_argument(
            "--input",
            required=True,
            help="Result file, run directory, or parent output directory.",
        )
        subparser.add_argument("--commit", help="Git commit SHA for this run.")
        subparser.add_argument("--run-url", help="GitHub Actions run URL.")

    local = subparsers.add_parser("local", help="Write to a local history folder.")
    add_common(local)
    local.add_argument(
        "--history-dir",
        default=str(DEFAULT_HISTORY_DIR),
        help=f"Local history directory. Default: {DEFAULT_HISTORY_DIR}",
    )

    branch = subparsers.add_parser(
        "branch", help="Write to the history branch and push it."
    )
    add_common(branch)
    branch.add_argument(
        "--branch",
        default=DEFAULT_BRANCH,
        help=f"History branch. Default: {DEFAULT_BRANCH}",
    )
    branch.add_argument(
        "--workdir",
        default=str(DEFAULT_BRANCH_WORKDIR),
        help=f"Temporary checkout for the history branch. Default: {DEFAULT_BRANCH_WORKDIR}",
    )
    branch.add_argument(
        "--repo",
        help="Git remote URL/path. Defaults to this repo's origin remote.",
    )

    return parser.parse_args()


def find_result_file(input_path: Path) -> Path:
    input_path = input_path.expanduser().resolve()
    if input_path.is_file():
        return input_path
    if not input_path.is_dir():
        raise SystemExit(f"input does not exist: {input_path}")

    direct = input_path / RESULT_FILE
    if direct.exists():
        return direct

    candidates = sorted(input_path.rglob(RESULT_FILE), key=result_sort_key)
    if not candidates:
        raise SystemExit(f"no {RESULT_FILE} found under {input_path}")
    return candidates[-1]


def result_sort_key(path: Path) -> tuple[str, float]:
    try:
        result = read_json(path)
        run_id = result.get("runID", "")
        match = RUN_ID_RE.match(run_id)
        if match:
            return (match.group(1) + match.group(2), path.stat().st_mtime)
        return (run_timestamp(result), path.stat().st_mtime)
    except Exception:
        return ("", path.stat().st_mtime)


def read_json(path: Path) -> Any:
    with path.open() as f:
        return json.load(f)


def append_history(
    history_dir: Path,
    result_path: Path,
    result: dict[str, Any],
    commit: str,
    run_url: str,
    metrics: list[dict[str, Any]],
) -> bool | None:
    history_dir.mkdir(parents=True, exist_ok=True)
    index_dir = history_dir / "index"
    index_dir.mkdir(parents=True, exist_ok=True)

    timestamp = run_timestamp(result)
    test_name = required_str(result, "testName")
    if is_duplicate(index_dir / "metrics.ndjson", test_name, timestamp):
        print(f"skip: {test_name} at {timestamp} already exists")
        return False

    run_id = required_str(result, "runID")
    result_relpath = result_history_path(result, timestamp)
    result_dir = history_dir / result_relpath.parent
    result_dir.mkdir(parents=True, exist_ok=True)

    shutil.copy2(result_path, history_dir / result_relpath)

    metric_records = []
    for metric in metrics:
        metric_records.append(
            {
                "testName": test_name,
                "runID": run_id,
                "runTimestamp": timestamp,
                "commit": commit,
                "runURL": run_url,
                "metric": metric["name"],
                "valueSeconds": metric["valueSeconds"],
                "unit": "s",
                "resultPath": str(result_relpath),
                "storedAt": now_utc(),
            }
        )
    append_ndjson(index_dir / "metrics.ndjson", metric_records)
    print(f"stored: {test_name} {run_id}")
    return True


def is_duplicate(runs_path: Path, test_name: str, timestamp: str) -> bool:
    if not runs_path.exists():
        return False
    with runs_path.open() as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            record = json.loads(line)
            if (
                record.get("testName") == test_name
                and record.get("runTimestamp") == timestamp
                and record.get("metric") == "total"
            ):
                return True
    return False


def result_history_path(result: dict[str, Any], timestamp: str) -> Path:
    dt = parse_timestamp(timestamp)
    test_name = safe_path_part(required_str(result, "testName"))
    run_id = safe_path_part(required_str(result, "runID"))
    return (
        Path("results")
        / test_name
        / f"{dt.year:04d}"
        / f"{dt.month:02d}"
        / f"{dt.day:02d}"
        / run_id
        / RESULT_FILE
    )


def run_timestamp(result: dict[str, Any]) -> str:
    for phase in result.get("phases", []):
        if phase.get("startTime"):
            return iso_seconds(phase["startTime"])

    run_id = required_str(result, "runID")
    match = RUN_ID_RE.match(run_id)
    if not match:
        raise ValueError(f"cannot derive timestamp from runID: {run_id}")
    return datetime.strptime(
        match.group(1) + match.group(2), "%Y%m%d%H%M%S"
    ).isoformat(timespec="seconds")


def parse_timestamp(value: str) -> datetime:
    parsed = datetime.fromisoformat(value)
    if parsed.tzinfo is None:
        return parsed
    return parsed.astimezone(parsed.tzinfo)


def iso_seconds(value: str) -> str:
    return datetime.fromisoformat(value).isoformat(timespec="seconds")


def flatten_metrics(result: dict[str, Any]) -> list[dict[str, Any]]:
    metrics = [
        {
            "name": "total",
            "valueSeconds": float(result["testDurationSeconds"]),
        }
    ]
    for phase in result.get("phases", []):
        phase_name = required_str(phase, "name")
        phase_duration = duration_seconds(phase["startTime"], phase["endTime"])
        metrics.append(
            {
                "name": f"phase.{phase_name}",
                "valueSeconds": phase_duration,
            }
        )
        for milestone in phase.get("milestones", []):
            milestone_name = required_str(milestone, "name")
            metrics.append(
                {
                    "name": f"milestone.{phase_name}.{milestone_name}",
                    "valueSeconds": float(milestone["durationFromPhaseStartSeconds"]),
                }
            )
    return metrics


def duration_seconds(start: str, end: str) -> float:
    return (datetime.fromisoformat(end) - datetime.fromisoformat(start)).total_seconds()


def append_ndjson(path: Path, records: list[dict[str, Any]]) -> None:
    with path.open("a") as f:
        for record in records:
            f.write(json.dumps(record, sort_keys=True, separators=(",", ":")))
            f.write("\n")


def copy_dashboard_files(history_dir: Path) -> None:
    if not DASHBOARD_DIR.is_dir():
        raise SystemExit(f"dashboard directory not found: {DASHBOARD_DIR}")
    history_dir.mkdir(parents=True, exist_ok=True)
    for name in ("index.html", "app.js", "style.css"):
        shutil.copy2(DASHBOARD_DIR / name, history_dir / name)


def prepare_branch_workdir(workdir: Path, branch: str, repo: str | None) -> Path:
    workdir = workdir.expanduser().resolve()
    repo = repo or default_repo_remote()

    if (workdir / ".git").exists():
        git(["fetch", "origin"], cwd=workdir)
    else:
        if workdir.exists() and any(workdir.iterdir()):
            raise SystemExit(f"workdir exists and is not empty: {workdir}")
        workdir.parent.mkdir(parents=True, exist_ok=True)
        git(["clone", repo, str(workdir)])
        git(["fetch", "origin"], cwd=workdir)

    if remote_branch_exists(workdir, branch):
        git(["checkout", "-B", branch, f"origin/{branch}"], cwd=workdir)
    else:
        git(["checkout", "--orphan", branch], cwd=workdir)
        clear_worktree(workdir)
    return workdir


def commit_and_push(
    history_dir: Path, branch: str, result: dict[str, Any], result_changed: bool
) -> None:
    ensure_git_identity(history_dir)
    git(["add", "results", "index", "index.html", "app.js", "style.css"], cwd=history_dir)
    if not git_has_changes(history_dir):
        print("no git changes to commit")
        return

    if result_changed:
        message = (
            f"Add scale test result {result.get('testName')} "
            f"{result.get('runID')}"
        )
    else:
        message = "Update scale test dashboard"
    git(["commit", "-m", message], cwd=history_dir)
    if remote_branch_exists(history_dir, branch):
        git(["fetch", "origin"], cwd=history_dir)
        git(["rebase", f"origin/{branch}"], cwd=history_dir)
    git(["push", "origin", f"HEAD:{branch}"], cwd=history_dir)
    print(f"pushed history branch: {branch}")


def ensure_git_identity(workdir: Path) -> None:
    if not best_effort_git(["config", "user.name"], cwd=workdir):
        git(["config", "user.name", "github-actions[bot]"], cwd=workdir)
    if not best_effort_git(["config", "user.email"], cwd=workdir):
        git(
            [
                "config",
                "user.email",
                "github-actions[bot]@users.noreply.github.com",
            ],
            cwd=workdir,
        )


def remote_branch_exists(workdir: Path, branch: str) -> bool:
    result = subprocess.run(
        ["git", "ls-remote", "--exit-code", "--heads", "origin", branch],
        cwd=workdir,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def clear_worktree(workdir: Path) -> None:
    for child in workdir.iterdir():
        if child.name == ".git":
            continue
        if child.is_dir():
            shutil.rmtree(child)
        else:
            child.unlink()


def git_has_changes(workdir: Path) -> bool:
    result = subprocess.run(
        ["git", "status", "--porcelain"],
        cwd=workdir,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
    )
    return bool(result.stdout.strip())


def default_repo_remote() -> str:
    root = Path(__file__).resolve().parents[2]
    remote = best_effort_git(["config", "--get", "remote.origin.url"], cwd=root)
    return remote or str(root)


def best_effort_git(args: list[str], cwd: Path | None = None) -> str:
    try:
        result = subprocess.run(
            ["git", *args],
            cwd=cwd,
            check=True,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        return ""


def git(args: list[str], cwd: Path | None = None) -> None:
    subprocess.run(["git", *args], cwd=cwd, check=True)


def required_str(obj: dict[str, Any], key: str) -> str:
    value = obj.get(key)
    if not isinstance(value, str) or not value:
        raise ValueError(f"missing required string field: {key}")
    return value


def safe_path_part(value: str) -> str:
    value = value.strip().replace("/", "-")
    return re.sub(r"[^A-Za-z0-9._-]+", "-", value) or "unknown"


def now_utc() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


if __name__ == "__main__":
    try:
        sys.exit(main())
    except subprocess.CalledProcessError as err:
        print(f"command failed: {' '.join(err.cmd)}", file=sys.stderr)
        sys.exit(err.returncode)
