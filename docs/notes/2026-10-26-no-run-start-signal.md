# No `run_start` signal

2026-10-26, the user, of the `run_start` signal kept through the rename:
"remove it — its old". It was v4's way to mark the chain a run starts with
(`on: run_start`, several entrypoints allowed); in v5 the main chain is that
by its section, and the parser only filled the signal in for it and refused it
everywhere else. Now the main chain has no opening signal (`Phase.On.Signal`
empty; `Phase.OpensOn()` names "the run's start" in messages), the registry
loses the entry and the `start` kind (`manual` is the one signal not
observed), and `on: run_start` is an unknown signal like any other. The API's
phase summaries carry an empty signal for the main chain; the editor tests a
chain's kind, not its signal.
