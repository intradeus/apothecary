package workspace

import "fmt"

// markerVar is the environment variable a marked command's process tree
// carries (ports.Command.Marker): what the next command with the same marker
// sweeps for before it starts.
const markerVar = "APOTHECARY_CMD"

// sweepPrelude is the shell preamble a marked command runs under: it kills
// whatever earlier command with the same marker is still running — TERM
// first, KILL a second later for whatever ignored it. Processes are found by
// the marker in their environment (every child inherits it; pgrep cannot
// match it, /proc can). The sweeping shell skips itself: it carries the
// marker too, by --remote-env.
func sweepPrelude(marker string) string {
	return fmt.Sprintf(`__apo_sweep() { local m='%[1]s=%[2]s' d p n=0
  for d in /proc/[0-9]*; do p=${d#/proc/}; [ "$p" = "$$" ] && continue
    if tr '\0' '\n' < "$d/environ" 2>/dev/null | grep -qxF -- "$m"; then kill -TERM "$p" 2>/dev/null || true; n=1; fi
  done
  [ "$n" = 1 ] || return 0
  sleep 1
  for d in /proc/[0-9]*; do p=${d#/proc/}; [ "$p" = "$$" ] && continue
    if tr '\0' '\n' < "$d/environ" 2>/dev/null | grep -qxF -- "$m"; then kill -KILL "$p" 2>/dev/null || true; fi
  done
}
__apo_sweep; unset -f __apo_sweep
`, markerVar, marker)
}
