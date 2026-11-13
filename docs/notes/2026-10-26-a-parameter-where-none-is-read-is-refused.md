# A `$parameter` where none is read is refused

2026-10-26, the user, finding parameters "not really dynamic": `description:
$note` on a watcher was accepted and kept the text "$note" — the only hint a
warning that nothing reads `note`. Parameters are read at their sites only
(typed, enum-checked); everywhere else `$name` is now refused when it is a
field's whole value (`notAParam`, naming the sites in its message): the
flow's, a stage's, a watcher's, an outcome's, a sequence's and a parameter's
descriptions, a sequence stage's, a gate's `question`, a `run:` script, an
agent's prompt, description, its inputs' and outputs' descriptions and its
outputs' paths, and every path a stage binds (the paths refused before, now
saying why). Names already refused it. A declared parameter named inside a
script or a prompt (`$name`, `${name}`) is a warning (`untemplatedWarnings`):
text with a `$` in it is legitimate — `echo $HOME`, a cost — but neither is
templated. The built-in templates raise none of it.
