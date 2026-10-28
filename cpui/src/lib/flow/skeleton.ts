import { CURRENT_VERSION } from '$lib/flowdoc';

// A minimal valid document: one agent does what the ticket asks (the ticket
// is always implicit) and the flow completes. Everything else is added in
// the editor.
export const flowSkeleton = (name: string) => `name: ${name}
version: ${CURRENT_VERSION}
description: describe what this flow does
# model has no default here: the workshop binds it (Flows → Configure), and a
# run may override it. Every agent stage names the harness and model it runs
# on — the parameters, or a literal.
parameters: { harness: opencode, timeout: 30m, max_turns: 50, max_cost_usd: 10.0, max_total_turns: 300 }
agents:
  # An agent is a prompt, the inputs a stage hands it and the files it writes.
  - name: worker
    description: does what the ticket asks
    inputs:
      - { name: failure, description: why the last attempt failed, optional: true }
    prompt: |
      Do what the TASK section asks, in this repository.
main:
  # The chain a run starts with. Every stage declares what it reads and
  # writes; $FAILURE is what the edge that sent the run back here carried.
  - { id: work, type: agent, agent: worker, harness: $harness, model: $model, on_success: done,
      input: { failure: $FAILURE },
      on_fail: { to: work, max: 2 } }
`;
