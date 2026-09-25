#!/bin/sh
# Runs every fuzz target for FUZZTIME each (default 20s). New failing inputs are
# saved under the package's testdata/fuzz/ and replayed by plain `go test`
# from then on: commit them as regression tests.
set -eu
fuzztime=${FUZZTIME:-20s}
status=0
for module in . auth; do
	for pkg in $(cd "$module" && go list ./...); do
		dir=$(cd "$module" && go list -f '{{.Dir}}' "$pkg")
		for target in $(grep -hoE '^func (Fuzz[A-Za-z0-9_]+)' "$dir"/*_test.go 2>/dev/null | cut -d' ' -f2); do
			echo "== $pkg $target"
			if ! (cd "$module" && go test -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" "$pkg"); then
				status=1
			fi
		done
	done
done
exit "$status"
