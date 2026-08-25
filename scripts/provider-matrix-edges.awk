# Turns `atenea catalog` into one "capability|implementation" line per declared
# edge of the matrix.
#
# This is a file of its own so the attribution can be tested against a fixture
# catalog without building and running the binary: the bug it replaces was
# invisible to any check that only ever saw a real, correct catalog.
#
# The catalog prints a capability header at column one and indents each of its
# implementations by exactly four spaces, with their own details indented by
# six:
#
#   capability code.search 1.0.0
#     implementations
#       codex.search (provider codex)
#         constraints  languages=- index=false vcs=false scale=-..-
#
# so the header sets the capability every following implementation line belongs
# to, and the six-space detail lines are excluded by requiring a non-space at
# column five.

/^capability / { capability = $2; next }

capability != "" &&
substr($0, 1, 4) == "    " &&
substr($0, 5, 1) != " " &&
index($0, " (provider ") > 0 {
	print capability "|" $1
}
