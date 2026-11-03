#!/bin/sh
# The commit convention, with nothing to install: the commit-msg hook
# (lefthook.yml) and CI's check of a pull request's title, which becomes the
# squashed commit on main. Usage: conventional.sh <message file>
#
#   <type>(<scope>)!: <subject>     scope and ! optional
#   (blank line, then any body)
#
# The same rule as .cz.toml's schema_pattern, which the release reads the
# history with; internal/devtools/commitmsg holds the two together. Git's
# comment lines and everything below a `commit -v` scissors line are not the
# message; an empty one is git aborting, not a bad message. Merges, reverts
# and fixups keep git's own subjects (commitizen's allowed prefixes).
exec awk '
/^# -+ >8 -+$/ { exit }
/^#/ { next }
{ sub(/\r$/, ""); line[++n] = $0 }
END {
	body = 0
	for (i = 1; i <= n; i++) if (line[i] ~ /[^[:space:]]/) { if (i == 1) subject = 1; else body = 1 }
	if (!subject && !body) exit 0
	if (line[1] ~ /^(Merge|Revert|Pull request|fixup!|squash!|amend!)/) exit 0
	ok = line[1] ~ /^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([^[:space:]]+\))?!?: .+$/
	if (ok && body && line[2] != "") {
		print "commit message: leave a blank line between the subject and the body" > "/dev/stderr"
		exit 1
	}
	if (!ok) {
		print "commit message: \"" line[1] "\" does not follow the convention:" > "/dev/stderr"
		print "  <type>(<scope>)!: <subject>   scope and ! optional, e.g. feat(flow)!: a run has one cursor" > "/dev/stderr"
		print "  types: feat fix chore perf refactor build revert (release) · docs test ci style (no release)" > "/dev/stderr"
		exit 1
	}
}' "$1"
