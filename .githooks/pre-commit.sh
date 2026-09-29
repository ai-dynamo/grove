#!/usr/bin/env sh
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

set -eu

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

snapshot_repo_state() {
	if git rev-parse --verify HEAD >/dev/null 2>&1; then
		diff_base="HEAD"
	else
		diff_base="$(printf '' | git hash-object -t tree --stdin)"
	fi

	{
		git diff --no-ext-diff --binary "$diff_base" --
		printf '\0'
		git ls-files --others --exclude-standard | LC_ALL=C sort
	} | git hash-object --stdin
}

before_state="$(snapshot_repo_state)"

make validate

after_state="$(snapshot_repo_state)"

if [ "$before_state" != "$after_state" ]; then
	echo "ERROR: make validate changed tracked diffs or the set of untracked files." >&2
	echo "Please review and stage those changes before committing." >&2
	exit 1
fi
