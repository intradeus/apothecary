# The workspace log is read by byte range (2026-11-10)

- The Logs pane read the last 64 KiB again on every poll. A refresh showed
  only that much of a long build. The provisioning copy is captured only
  when provisioning ends, so it was no help while it ran.
- `envport.ExecLogTailer` is now `ReadExecLog(from, n)`: up to n bytes from
  an offset, or the tail when from is negative.
  `envport.ReadExecLogRange` is the one implementation both adapters
  share. It moves both edges back to the start of a UTF-8 character, so
  ranges read back to back splice into the log exactly. A character still
  being written comes with the next read.
- `GET /api/runs/{id}/workspace-log` answers JSON `{data, start, end, size}`
  and takes `from`. It reads at most 8 MiB per call (`wsinfo.MaxLogRead`).
- The pane reads the last 2 MiB once, then only what was written since
  `end` (1 MiB per poll), one read at a time. A `size` below `end` means
  the log was replaced: it reads the tail again. "Load earlier output"
  reads the 2 MiB ending at `start` and keeps the reader's place.
- A new `InfoTip` (`$lib/ui`): a ⓘ beside a field's label. The tooltip is
  the button's `aria-describedby`, and it is portalled to `<body>` so glass
  doesn't clip it. The first use is the services editor's Stop field: no
  stop command means SIGTERM to the start command's process group, then
  SIGKILL 10 s later.
