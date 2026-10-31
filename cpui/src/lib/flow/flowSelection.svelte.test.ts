import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { FlowDoc } from '$lib/domain/flowdoc';
import { readDoc } from '$test/fixtures/logic';
import { FlowEditor } from './flowEditor.svelte';
import type { FlowSelection } from './flowSelection.svelte';

vi.mock('$lib/api', () => ({ api: {} }));
vi.mock('$lib/ui/toast.svelte', () => ({ toast: vi.fn() }));
vi.mock('$lib/ui/confirm.svelte', () => ({ confirmDialog: vi.fn() }));

const doc = (): FlowDoc =>
	readDoc(`
name: f
version: 5
parameters: {}
agents: [{ name: writer, prompt: p }]
sequences:
  - id: inner
    stages:
      - { id: leaf, type: command, run: r }
  - id: outer
    stages:
      - { id: nested, sequence: inner }
      - { id: tail, type: command, run: t }
main:
  - { id: plain, type: command, run: r }
  - { id: runs, sequence: outer, on_success: done }
watchers:
  - { id: w, on: manual, stages: [{ id: x, type: command, run: x }] }
`);

let ed: FlowEditor;
let sel: FlowSelection;

beforeEach(() => {
	ed = new FlowEditor(
		() => 'p',
		() => 'f'
	);
	ed.doc = doc();
	sel = ed.panels;
});

const stage = (id: string, sequence?: string) => ({ kind: 'stage', id, sequence }) as const;

describe('select', () => {
	it('selects from the canvas, starting the trail afresh', () => {
		sel.trail = [stage('plain')];
		sel.select(stage('runs'));
		expect(sel.selection).toEqual({ kind: 'stage', id: 'runs' });
		expect(sel.trail).toEqual([]);
	});

	it('keeps the trail when told to', () => {
		sel.trail = [stage('plain')];
		sel.select(stage('runs'), true);
		expect(sel.trail).toHaveLength(1);
	});

	it('opens the sequence a stage runs', () => {
		sel.select(stage('runs'));
		expect(sel.pane).toEqual({ root: 'runs', path: [] });
		expect(sel.open?.sequence.id).toBe('outer');
	});

	it('keeps the pane where it is on the same sequence stage', () => {
		sel.pane = { root: 'runs', path: ['nested'] };
		sel.select(stage('runs'));
		expect(sel.pane.path).toEqual(['nested']);
	});

	it('closes the pane for a plain stage', () => {
		sel.select(stage('runs'));
		sel.select(stage('plain'));
		expect(sel.pane).toBeNull();
	});

	it('closes the pane for what is not in a sequence', () => {
		sel.select(stage('runs'));
		sel.select({ kind: 'phase', id: 'w' });
		expect(sel.pane).toBeNull();
		sel.select(stage('runs'));
		sel.select(null);
		expect(sel.pane).toBeNull();
	});

	it('opens a sequence on its own', () => {
		sel.select({ kind: 'sequence', id: 'inner' });
		expect(sel.pane).toEqual({ root: '', sequence: 'inner', path: [] });
	});

	it('opens a sequence’s stage or edge in its sequence', () => {
		sel.select(stage('leaf', 'inner'));
		expect(sel.pane?.sequence).toBe('inner');
		sel.select({ kind: 'edge', stage: 'tail', edge: 'on_fail', sequence: 'outer' });
		expect(sel.pane?.sequence).toBe('outer');
	});

	it('goes back up to a sequence already open above', () => {
		sel.select(stage('runs'));
		sel.pane = { root: 'runs', path: ['nested'] };
		expect(sel.levels).toHaveLength(2);
		sel.select({ kind: 'sequence', id: 'outer' });
		expect(sel.pane).toEqual({ root: 'runs', path: [] });
	});

	it('sends an agent to the Agents tab', () => {
		sel.select({ kind: 'agent', name: 'writer' });
		expect(ed.tab).toBe('agents');
		expect(ed.agentOpen).toBe('writer');
		expect(sel.selection).toBeNull();
	});

	it('holds the selection with no document yet', () => {
		ed.doc = null;
		sel.select(stage('runs'));
		expect(sel.selection).toEqual({ kind: 'stage', id: 'runs' });
		expect(sel.pane).toBeNull();
	});
});

describe('the trail', () => {
	it('leaves a step behind when a panel opens from another', () => {
		sel.select(stage('plain'));
		sel.openPanel({ kind: 'phase', id: 'w' });
		expect(sel.trail).toEqual([{ kind: 'stage', id: 'plain' }]);
		expect(sel.selection).toEqual({ kind: 'phase', id: 'w' });
	});

	it('goes to the Agents tab without leaving a step', () => {
		sel.select(stage('plain'));
		sel.openPanel({ kind: 'agent', name: 'writer' });
		expect(sel.trail).toEqual([]);
		expect(ed.tab).toBe('agents');
	});

	it('goes back a step at a time', () => {
		sel.select(stage('plain'));
		sel.openPanel({ kind: 'phase', id: 'w' });
		sel.openPanel(stage('x'));
		sel.goBack();
		expect(sel.selection).toEqual({ kind: 'phase', id: 'w' });
		expect(sel.trail).toHaveLength(1);
		sel.goBack();
		expect(sel.selection).toEqual({ kind: 'stage', id: 'plain' });
		expect(sel.trail).toEqual([]);
	});

	it('skips a step that was deleted', () => {
		sel.select(stage('plain'));
		sel.openPanel({ kind: 'phase', id: 'w' });
		sel.openPanel(stage('x'));
		ed.doc!.phases = ed.doc!.phases!.filter((p) => p.id !== 'w');
		sel.goBack();
		expect(sel.selection).toEqual({ kind: 'stage', id: 'plain' });
	});

	it('clears everything when nothing is left to go back to', () => {
		sel.selection = stage('x');
		sel.trail = [stage('gone')];
		sel.goBack();
		expect(sel.selection).toBeNull();
		expect(sel.trail).toEqual([]);
	});

	it('replaces a panel, the trail kept', () => {
		sel.select(stage('plain'));
		sel.openPanel(stage('runs'));
		sel.replace(stage('x'));
		expect(sel.selection).toEqual({ kind: 'stage', id: 'x' });
		expect(sel.trail).toHaveLength(1);
	});

	it('renames the trail along with a rename', () => {
		sel.select({ kind: 'sequence', id: 'outer' });
		sel.openPanel(stage('tail', 'outer'));
		sel.openPanel({ kind: 'sequence', id: 'outer' });
		sel.replace({ kind: 'sequence', id: 'renamed' }, 'rename');
		expect(sel.trail).toEqual([
			{ kind: 'sequence', id: 'renamed' },
			{ kind: 'stage', id: 'tail', sequence: 'renamed' }
		]);
	});
});

describe('the open sequence', () => {
	it('opens only on the graph', () => {
		sel.select(stage('runs'));
		expect(sel.levels).toHaveLength(1);
		ed.tab = 'yaml';
		expect(sel.levels).toEqual([]);
		expect(sel.open).toBeUndefined();
		expect(sel.sequenceDoc).toBeNull();
	});

	it('is a document of its own: the flow’s, with its stages and no chains', () => {
		sel.select(stage('runs'));
		expect(sel.sequenceDoc?.stages.map((s) => s.id)).toEqual(['nested', 'tail']);
		expect(sel.sequenceDoc?.phases).toBeUndefined();
		expect(sel.sequenceDoc?.agents).toHaveLength(1);
	});

	it('is drawn afresh when it changes, not otherwise', () => {
		expect(sel.paneKey).toBe('');
		sel.select(stage('runs'));
		expect(sel.paneKey).toBe('outer|nested,tail');
		sel.pane = { root: 'runs', path: ['nested'] };
		expect(sel.paneKey).toBe('outer/inner|leaf');
	});

	it('lays its stages out, newly on every change', () => {
		sel.select(stage('runs'));
		const rev = sel.sequenceRev;
		sel.relayoutSequence();
		expect(sel.sequenceLayout.tail.y).toBeGreaterThan(sel.sequenceLayout.nested.y);
		expect(sel.sequenceRev).toBe(rev + 1);
	});

	it('lays out nothing with nothing open', () => {
		sel.relayoutSequence();
		expect(sel.sequenceLayout).toEqual({});
		expect(sel.sequenceRev).toBe(0);
	});

	it('goes by what was measured, until the pane shows something else', () => {
		sel.select(stage('runs'));
		sel.onSequenceMeasured({ nested: { width: 100, height: 500 } });
		const tall = sel.sequenceLayout.tail.y;
		expect(tall).toBeGreaterThan(500);
		sel.resetSequenceLayout();
		expect(sel.sequenceLayout.tail.y).toBeLessThan(tall);
	});

	it('relays out with the document', () => {
		sel.select(stage('runs'));
		const rev = sel.sequenceRev;
		ed.markChanged();
		expect(sel.sequenceRev).toBe(rev + 1);
	});
});

describe('a click in the pane', () => {
	beforeEach(() => {
		sel.select(stage('runs'));
	});

	it('edits a stage in its sequence', () => {
		sel.trail = [stage('plain')];
		sel.onSequenceSelect(stage('tail'));
		expect(sel.selection).toEqual({ kind: 'stage', id: 'tail', sequence: 'outer' });
		expect(sel.trail).toEqual([]);
		expect(sel.pane?.path).toEqual([]);
	});

	it('opens a sequence stage in its place', () => {
		sel.onSequenceSelect(stage('nested'));
		expect(sel.pane?.path).toEqual(['nested']);
		expect(sel.open?.sequence.id).toBe('inner');
	});

	it('edits an edge in its sequence', () => {
		sel.onSequenceSelect({ kind: 'edge', stage: 'tail', edge: 'on_fail' });
		expect(sel.selection).toEqual({
			kind: 'edge',
			stage: 'tail',
			edge: 'on_fail',
			sequence: 'outer'
		});
	});

	it.each([[null], [{ kind: 'phase', id: 'x' } as const]])(
		'selects the sequence itself for anything else: %j',
		(what) => {
			sel.onSequenceSelect(what);
			expect(sel.selection).toEqual({ kind: 'sequence', id: 'outer' });
		}
	);

	it('does nothing with no pane open', () => {
		sel.pane = null;
		sel.selection = null;
		sel.onSequenceSelect(stage('tail'));
		expect(sel.selection).toBeNull();
	});
});
