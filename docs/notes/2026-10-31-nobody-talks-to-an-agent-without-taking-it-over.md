# Nobody talks to an agent without taking it over (2026-10-31)

The whisper and the interrupt are gone. Ask (read-only, beside the run) and
take-over (the agent in your hands) cover what they were for, and an open input
beside a running agent invited a third, timing-dependent way in.
`Engine.SendMessage` refuses without a hold (`ErrNotHeld`, 409), including while
one is being released, and the run page shows only *Take over* until there is
one. `Interrupt` left `agentport`, the ACP runner, `message_queued`, the API and
the UI; an old log's `interrupt` field is ignored on decode. The runner still
cancels a turn when the engine's context ends (timeout, park, abort, outcome).
