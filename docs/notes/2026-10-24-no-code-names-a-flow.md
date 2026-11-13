# No code names a flow

2026-10-24, the user: "Why do we need DefaultFlowName in the code? … No
backend code must use \"builder\" anywhere; it should just be used for
display / in a dropdown." `builtin.DefaultFlowName` is gone, and with it the
fallback that gave a project created without a `default_flow` the built-in
template: `default_flow` is now required (400 "default_flow is required"),
always the project's choice — the project form's dropdown lists the flows it
can run, templates included, and preselects the first. Templates are found
like any flow, by the name a project or a run gives (`builtin.Flow(name)`,
every `flows/*.yaml`). The `-dev` seed, which must give its project a flow
that resolves before the project's own `dev` flow can exist, borrows
whichever template `builtin.FlowNames()` lists first. Tests name the template
they exercise.
