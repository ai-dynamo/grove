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

"""infra_manager - Grove cluster infrastructure management package."""

from __future__ import annotations

import io
import logging
import threading
from contextlib import contextmanager

from rich.console import Console


class ThreadAwareConsole:
    """Console proxy that routes to thread-local buffers when set."""

    def __init__(self, real_console: Console) -> None:
        object.__setattr__(self, "_real", real_console)
        object.__setattr__(self, "_local", threading.local())

    def __getattr__(self, name: str):
        target = getattr(self._local, "console", self._real)
        return getattr(target, name)

    @contextmanager
    def buffered(self):
        """Buffer all console output for the current thread."""
        buf = io.StringIO()
        self._local.console = Console(file=buf, stderr=False)
        try:
            yield buf
        finally:
            del self._local.console


console = ThreadAwareConsole(Console(stderr=True))
logger = logging.getLogger("infra_manager")
