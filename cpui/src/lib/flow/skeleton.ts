import { CURRENT_VERSION } from '$lib/flowdoc';

// A minimal valid document: the builder gets the ticket (always implicit)
// and the flow completes. Everything else is added in the editor.
export const flowSkeleton = (name: string) => `name: ${name}
version: ${CURRENT_VERSION}
description: describe what this flow does
parameters: { timeout: 30m, harness: opencode, model: anthropic/claude-sonnet-4-6, max_turns: 50, max_cost_usd: 10.0, max_total_turns: 300 }
stages:
  # Every stage declares what it reads and writes; $FAILURE is what the edge
  # that sent the run back here carried.
  - { id: build, type: agent, agent: builder, on_success: done,
      input: { failure: $FAILURE },
      on_fail: { to: build, max: 2 } }
`;
