#!/usr/bin/env bash
set -euo pipefail

# Refuses a commit whose staged Go files gofmt would rewrite.
#
# This is a script rather than a shell snippet inside lefthook.yml because the
# snippet interpolated lefthook's `{staged_files}` straight into the command
# line, unquoted. A path containing a space arrived at gofmt as two arguments,
# both of which name nothing, so the hook failed with "no such file or
# directory" on a tree that was perfectly formatted -- and a hook that fails for
# the wrong reason is a hook people learn to pass with --no-verify. The same
# expansion also handed gofmt the paths of files staged for deletion, which by
# definition are no longer on disk.
#
# Asking git directly fixes both: --diff-filter=d drops the deletions, and the
# NUL separator carries every path through whatever it contains.

cd "$(git rev-parse --show-toplevel)"

staged=()
while IFS= read -r -d '' file; do
	staged+=("$file")
done < <(git diff --cached --name-only --diff-filter=d -z -- '*.go')

# No staged Go file is not a reason to run gofmt with no arguments: it would
# read stdin and hang the commit waiting for a program nobody is typing.
if [[ "${#staged[@]}" -eq 0 ]]; then
	exit 0
fi

unformatted="$(gofmt -l -- "${staged[@]}")"
if [[ -n "$unformatted" ]]; then
	echo "gofmt would rewrite:" >&2
	echo "$unformatted" >&2
	exit 1
fi
