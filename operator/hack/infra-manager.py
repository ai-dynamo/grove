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
infra-manager.py - Unified CLI for Grove infrastructure management.

Subcommands:
    setup      Composite workflows (e2e, scale, apply-topology, prepull-images)

Examples:
    # Full e2e setup (default — no flags needed!)
    ./infra-manager.py setup e2e

    # Scale test setup
    ./infra-manager.py setup scale --kwok-nodes 1000

For detailed usage information, run: ./infra-manager.py --help
"""

from __future__ import annotations

import logging
import sys

import typer
from infra_manager import console
from infra_manager.commands import setup_cmd

app = typer.Typer(
    help="Unified CLI for Grove infrastructure management.",
    no_args_is_help=True,
)


@app.callback()
def _main_callback() -> None:
    """Initialize logging for all subcommands."""
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%H:%M:%S",
    )


app.add_typer(setup_cmd.app, name="setup")


if __name__ == "__main__":
    try:
        app()
    except Exception as e:
        console.print(f"[red]\u274c {e}[/red]")
        sys.exit(1)
