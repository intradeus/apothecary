// Flow documents for the tests of the non-component logic: a document written
// the way a person writes it, read the way the editor reads it.
import { parse } from 'yaml';
import { docToYaml, normalizeDoc, type DocStage, type FlowDoc } from '$lib/domain/flowdoc';

/** A flow's YAML, read the way the editor reads a pinned document. */
export const readDoc = (yaml: string): FlowDoc => normalizeDoc(parse(yaml));

/** The document after one trip through the editor: written, then read again. */
export const throughEditor = (doc: FlowDoc): FlowDoc => normalizeDoc(parse(docToYaml(doc)));

/** The YAML the editor writes, as an object: what the engine gets to read. */
export const written = (doc: FlowDoc) => parse(docToYaml(doc));

export const aStage = (over: Partial<DocStage> & { id: string }): DocStage => ({
	type: 'command',
	...over
});

/** A flow of one main chain. `extra` adds what the test is about. */
export const aFlow = (stages: DocStage[], extra: Partial<FlowDoc> = {}): FlowDoc =>
	normalizeDoc({ name: 'flow', version: 5, parameters: {}, main: stages, ...extra });

/** Every section the grammar has, each with the fields it takes: what a
 *  round trip through the editor must not lose. */
export const EVERY_FIELD = `name: everything
version: 5
description: a flow that says everything once
operates_on: merge_request
ask: { harness: $harness }
parameters:
  harness: opencode
  model: { description: the model, enum: [fake/a, fake/b] }
  timeout: 30m
  max_turns: 50
  max_cost_usd: 10.5
  max_total_turns: 300
  plan_gate: { type: bool, default: false, description: a person reads the plan }
  retries: { type: int, default: 3 }
  ratio: { type: float, default: 0.5 }
  quiet: { type: duration, default: 10m }
  policy: { type: string, default: confirm, enum: [auto, confirm, manual] }
  labels: { type: string, list: true, default: [one, two] }
  app: { type: string, default: default }
  backup_model: { type: string, default: $model }
schemas:
  - name: report
    description: what a report holds
    json_schema: { type: object, properties: { title: { type: string } }, required: [title] }
mcp_servers:
  - name: browser
    description: the project's browser
    builtin: browser
  - name: tracker
    command: [npx, -y, "@acme/tracker-mcp"]
    env: { TRACKER_TOKEN: "\${secrets.tracker_token}" }
  - name: docs
    url: https://docs.test/mcp
    headers: { Authorization: "Bearer \${secrets.docs_token}" }
agents:
  - name: planner
    description: plans the work
    read_only: true
    allow_bash: true
    mcp: [browser, docs]
    inputs:
      - { name: ticket, description: the task, type: ticket }
      - { name: failure, optional: true, type: failure }
      - { name: notes, type: file, schema: report }
    outputs:
      - { name: plan, description: the plan, type: file }
      - { name: verdict, type: verdict, schema: report }
    prompt: |
      Plan the work.
      Two lines of it.
sequences:
  - id: ship
    description: test, then push
    stages:
      - id: test
        type: command
        run_ref: test
        on_fail: { to: fix, max: 2, then: fail }
      - id: fix
        type: agent
        agent: planner
        harness: $harness
        model: $model
        on_success: test
      - id: push
        type: action
        action: push
        on_fail: fail
  - id: outer
    stages:
      - { id: inner, sequence: ship, on_fail: { to: fix_outer, max: 1 } }
      - { id: fix_outer, type: command, run: make fix, on_success: end }
main:
  - id: plan
    type: agent
    description: makes the plan
    run_if: not $plan_gate
    agent: planner
    model: $model
    harness: $harness
    max_turns: $max_turns
    reuse_session: false
    timeout: $timeout
    input:
      ticket: { from: run.ticket }
      failure: { from: edge.failure, optional: true }
      notes: { from: docs/notes.md, schema: report, description: the notes }
    output:
      plan: { to: artifacts/plan.md, type: file }
      verdict: { to: artifacts/verdict.json, type: verdict, schema: report }
    on_success: gate
    on_fail: { to: plan, max: 2, then: { to: repair, max: 1, then: debug } }
    on_reject: { to: plan, max: -1 }
    on_timeout: abort
  - id: gate
    type: human_gate
    question: Good plan?
    input:
      plan: { from: artifacts/plan.md, type: file }
      link: { from: run.mr.url }
    on_success: build
    on_reject: { to: plan, max: 3 }
  - id: build
    type: command
    run: make build
    each_repo: true
    timeout: 5m
    output:
      binary: { to: out/, type: files }
    on_success: make_ship
    on_timeout: { to: repair, max: 1 }
  - id: make_ship
    sequence: ship
    description: runs ship
    on_success: serve
    on_fail: { to: repair, max: 1 }
  - id: serve
    type: service
    service: $app
    do: restart
    timeout: 1m
    on_success: wait_ci
    on_fail: abort
  - id: wait_ci
    type: wait
    for: ci_passed
    timeout: 2h
    on_success: open_mr
  - id: open_mr
    type: action
    action: create_mr
    with: { title: A title, labels: $labels, kinds: [a, b] }
    input:
      body: { from: artifacts/plan.md, type: file }
    on_success: end
  - id: repair
    type: command
    run_ref: repair
    on_success: plan
watchers:
  - id: conflicts
    description: keeps the branch mergeable
    on: { signal: mr_conflicts, start: $policy, quiet_for: $quiet, max: $retries }
    stages:
      - { id: resolve, type: command, run: make resolve, on_success: end }
  - id: by_hand
    on: manual
    sequence: outer
  - id: comments
    on: mr_comments
    stages:
      - { id: first, type: command, run: "true" }
      - { id: second, type: command, run: "false" }
outcomes:
  - id: merged
    description: the merge happened
    on: mr_merged
    stages:
      - { id: finish, type: action, action: ticket_done }
  - id: closed
    on: mr_closed
    then: abort
  - id: shipped
    on: { signal: mr_merged, start: confirm }
    sequence: ship
    then: abort
`;
