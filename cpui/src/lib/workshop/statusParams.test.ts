import { describe, expect, it } from 'vitest';
import type { ActionSpec } from '$lib/domain';
import { statusParams } from './statusParams';

const action = (slug: string, settings: ActionSpec['settings']) =>
	({
		slug,
		title: slug,
		single: false,
		remote: true,
		inputs: [],
		outputs: [],
		settings
	}) as ActionSpec;
const actions = [
	action('ticket_transition', [{ name: 'to', kind: 'text', required: true, unbound: true }]),
	action('ticket_set_field', [{ name: 'field', kind: 'text', required: true }])
];

const yaml = `
name: builder
main:
  - id: start
    type: action
    action: ticket_transition
    with: { to: $in_progress_status }
  - id: field
    type: action
    action: ticket_set_field
    with: { field: $field_name }
  - id: again
    type: action
    action: ticket_transition
    with: { to: $in_progress_status }
  - id: literal
    type: action
    action: ticket_transition
    with: { to: Done }
  - id: ship
    sequence: ship
sequences:
  - id: ship
    stages:
      - id: close
        type: action
        action: ticket_transition
        with: { to: $done_status }
      - id: build
        type: command
        run: make
`;

describe('statusParams', () => {
	it('lists the parameters read where a setting may wait for a status, once each', () => {
		expect(statusParams(yaml, actions)).toEqual(['in_progress_status', 'done_status']);
	});

	it('knows none without the action specs', () => {
		expect(statusParams(yaml, [])).toEqual([]);
	});

	it('finds none in a document that does not parse', () => {
		expect(statusParams('main: [', actions)).toEqual([]);
		expect(statusParams('', actions)).toEqual([]);
	});
});
