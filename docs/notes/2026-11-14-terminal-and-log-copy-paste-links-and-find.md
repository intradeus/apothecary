# Terminal and log copy, paste, links and find (2026-11-14)

- **Why:** copying out of the run's terminal and logs was a fight. The
  terminal took every Ctrl key for the shell, its URLs were dead text, and
  the logs scrolled to the bottom on every 2.5 s tail, which carried a
  selection away mid-drag. Neither had a way to jump to either end, and the
  logs had no find.
- **Terminal keys** (`cpui/src/lib/run/terminalKeys.ts`; the owner chose
  this scheme when asked):
  - off a Mac, Ctrl+Shift+C and Ctrl+Shift+V always copy and paste;
  - Ctrl+C copies when text is selected and is ^C when none is. After
    selecting, a click clears the selection before ^C interrupts again;
  - Ctrl+V pastes, so bash's quoted-insert (^V) is given up;
  - on a Mac every key passes to xterm: Cmd+C and Cmd+V already copy and
    paste through the browser, and Ctrl+C / Ctrl+V stay the shell's.
  - Copy writes `term.getSelection()` itself and calls `preventDefault`:
    Chromium opens its inspector on Ctrl+Shift+C otherwise. Paste only
    keeps the key from xterm and lets the browser's own paste event reach
    xterm's textarea, which needs no clipboard permission and keeps
    bracketed paste. The bar's Paste button needs clipboard-read; when the
    browser refuses, its toast names the shortcut.
  - Some browsers may keep Ctrl+Shift+C for themselves before the page sees
    it; the Copy button and Ctrl+C-with-a-selection still copy.
- **Links open on a plain click,** as in a browser: `@xterm/addon-web-links`
  in the terminal (a new tab, `noopener,noreferrer`), anchors in the logs.
  Log anchors are `draggable="false"`, so a drag across one selects text.
  `linkify` (`cpui/src/lib/core/links.ts`, moved out of `IntegrationForm`)
  matches http(s) only, so no `javascript:` link can come out of a log.
  A quote (`"`, `'`, `` ` ``) ends a URL: logs and shells quote URLs
  (`git clone "https://…"`), and a raw quote is no part of one. Unlike the
  copy in `IntegrationForm`, which kept the closing quote in the link.
- **Find, not filter:** every line stays on screen; matches are
  highlighted with an "n of m" count, and Enter / Shift+Enter or ↑ / ↓ step
  through them, wrapping. Lines around a match are what one reads a log
  for, which a filter would hide. Ctrl/Cmd+F inside a log opens it instead
  of the browser's find; Esc closes it and calls `preventDefault`, so the
  desk's Esc does not also restore a maximised window. As the tail grows,
  the current match stays the same match (it is kept by line and column).
- **Find on a long log, measured** (production build, headless Chromium,
  a 2.6 MB log of 30,000 lines). Every match drawn as a `<mark>` froze the
  page for as long as the DOM work took: "s" (180,000 matches) 7.2 s,
  "harness" (30,000) 4.6 s, each Enter / Shift+Enter 5.4 s. Now:
  - a row nothing changed stays the same object, so a step or a tail line
    re-renders only the rows it changed (a step: about 0.1 s);
  - the search runs once typing pauses for 200 ms, or at once on Enter (the
    plan's allowed fix for typing lag);
  - the count covers every match, but only the 300 matches either side of
    the current one are drawn, and stepping moves that window. It counts
    matches, not lines, so one long line with a broad query is held to it
    too. The plan allowed only the debounce; the owner chose this when
    asked, as the debounce does nothing for a broad query. "s" now takes
    0.54 s and "harness" 0.17 s (measured with a 300-line window, which on
    that log draws at least as many marks as this one).
- **No following while text is selected,** nor while a match is current:
  `LogOutput` follows the bottom only if the reader is within 40 px of it,
  holds no non-collapsed selection inside the box, and is on no match.
  "To bottom" follows again; "to top" stops following.
- **One log component:** the Logs pane and each Services card share
  `cpui/src/lib/ui/LogOutput.svelte` (find, copy, to top / to bottom,
  following). It knows nothing of runs; the parent's controls go into its
  toolbar through a snippet.
- **Accepted:** rows are keyed by index, so "Load earlier" re-texts every
  row and drops an active selection; appends leave unchanged rows alone.
  Over plain http (no secure context) the copy buttons fail into the
  refusal toast; keyboard paste still works.
- **Tests:** `run/terminalKeys.test.ts` (the scheme, keydown and keyup,
  Linux and macOS, non-Latin layouts), `run/Terminal.test.ts` (links, key
  handler, Copy / Paste / top / bottom buttons, refusals, cleanup),
  `core/links.test.ts`, `ui/logSegments.test.ts`, `ui/LogOutput.test.ts`
  (links, copy, find with its pause and its 300-match window, following with
  and without a selection, Esc `defaultPrevented`),
  `run/views/LogsView.test.ts` (a source switch follows
  again) and `run/views/ServicesView.test.ts` (links, copy, following).
