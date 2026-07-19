# greeter — agent guide

Tiny Go library used as the factory's example target project.

## Layout

- `greeter.go` — the library: pure functions, no I/O.
- `greeter_test.go` — table-driven tests, one table per function.
- `cmd/greet/main.go` — the only place allowed to print.

## Conventions

- Pure functions in the library; side effects live in `cmd/`.
- Every exported function has a doc comment and a table-driven test.
- Errors are returned, never logged from the library.
- Run `make verify` before considering any change done; it must be green.
- Commit style: imperative subject, no trailing period.
