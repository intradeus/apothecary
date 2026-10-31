import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { BeforeNavigate } from '@sveltejs/kit';
import { goto } from '$app/navigation';
import { api } from '$lib/api';
import type { Flow, FlowValidation, FlowVersion } from '$lib/domain';
import { docToYaml } from '$lib/domain/flowdoc';
import { confirmDialog } from '$lib/ui/confirm.svelte';
import { toast } from '$lib/ui/toast.svelte';
import { readDoc } from '$test/fixtures/logic';
import { deferred } from '$test/helpers';
import { FlowEditor } from './flowEditor.svelte';

vi.mock('$lib/api', () => ({
	api: {
		flows: {
			get: vi.fn(),
			versions: vi.fn(),
			validate: vi.fn(),
			schema: vi.fn(),
			save: vi.fn(),
			create: vi.fn(),
			remove: vi.fn(),
			version: vi.fn()
		},
		system: { actions: vi.fn(), signals: vi.fn() }
	}
}));
vi.mock('$lib/ui/toast.svelte', () => ({ toast: vi.fn() }));
vi.mock('$lib/ui/confirm.svelte', () => ({ confirmDialog: vi.fn() }));

const YAML = `name: builder
version: 5
parameters: {}
main:
  - { id: a, type: command, run: a }
  - { id: b, type: command, run: b }
`;
const NEWER = YAML.replace('run: a', 'run: changed');

const aFlowRow = (over: Partial<Flow> = {}): Flow => ({
	projectId: 'p1',
	name: 'builder',
	yaml: YAML,
	hash: 'aaaaaaaa11111111',
	updatedAt: '',
	builtin: false,
	shadowsBuiltin: false,
	parameters: [],
	phases: [],
	...over
});
const savedFlow = (over: Partial<Flow> = {}) =>
	aFlowRow({ yaml: NEWER, hash: 'bbbbbbbb22222222', ...over });

const valid = (yaml = YAML, over: Partial<FlowValidation> = {}): FlowValidation => ({
	ok: true,
	errors: [],
	warnings: [],
	parameters: [],
	refs: [],
	phases: [],
	stages: 2,
	hash: 'h',
	doc: readDoc(yaml),
	...over
});
const refused = (errors: string[] = ['line 3: nope']): FlowValidation => ({
	ok: false,
	errors,
	warnings: [],
	parameters: [],
	refs: [],
	phases: []
});

const flows = vi.mocked(api.flows);
const wireCopy = <T>(v: T): T => JSON.parse(JSON.stringify(v));

async function opened(row: Flow = aFlowRow()) {
	flows.get.mockResolvedValue(row);
	const ed = new FlowEditor(
		() => 'p1',
		() => row.name
	);
	await ed.load();
	return ed;
}

/** A change made on the canvas. */
function edit(ed: FlowEditor) {
	ed.doc!.stages[0].run = 'changed';
	ed.markChanged();
}

beforeEach(() => {
	flows.versions.mockResolvedValue([]);
	flows.validate.mockImplementation(async (yaml) => valid(yaml));
	flows.schema.mockResolvedValue({ type: 'object' });
	vi.mocked(api.system.actions).mockResolvedValue([]);
	vi.mocked(api.system.signals).mockResolvedValue([]);
});

describe('opening a flow', () => {
	it('loads the flow, its text, its document and its history', async () => {
		const versions: FlowVersion[] = [{ hash: 'h', name: 'builder', yaml: YAML, createdAt: '' }];
		flows.versions.mockResolvedValue(versions);
		const ed = await opened();
		expect(flows.get).toHaveBeenCalledWith('p1', 'builder');
		expect(ed.flow?.name).toBe('builder');
		expect(ed.yaml).toBe(YAML);
		expect(ed.doc?.stages.map((s) => s.id)).toEqual(['a', 'b']);
		expect(ed.stageCount).toBe(2);
		expect(ed.versions).toEqual(versions);
		expect(ed.dirty).toBe(false);
		expect(ed.rev).toBe(1);
	});

	it('lays out a flow with no stored layout', async () => {
		const ed = await opened();
		expect(ed.layout.a).toBeDefined();
		expect(ed.layout.b.x).toBeGreaterThan(ed.layout.a.x);
	});

	it('keeps the stored layout', async () => {
		const nodes = { a: { x: 7, y: 8 } };
		const ed = await opened(aFlowRow({ layout: { nodes } }));
		expect(ed.layout).toEqual(nodes);
	});

	it('notes the warnings of a valid flow', async () => {
		flows.validate.mockResolvedValue(valid(YAML, { warnings: ['w'], stages: 2, hash: 'h' }));
		const ed = await opened();
		expect(ed.validation).toEqual({
			ok: true,
			title: 'Valid, with binding warnings',
			stages: 2,
			hash: 'h',
			warnings: ['w']
		});
	});

	it('opens the text of a flow that does not parse, saying why', async () => {
		flows.validate.mockResolvedValue(refused());
		const ed = await opened();
		expect(ed.tab).toBe('yaml');
		expect(ed.doc).toBeNull();
		expect(ed.validateOpen).toBe(true);
		expect(ed.validation).toEqual({
			ok: false,
			title: 'This flow does not parse',
			errors: ['line 3: nope']
		});
	});

	it('shows why a flow would not load', async () => {
		flows.get.mockRejectedValue(new Error('no such flow'));
		const ed = new FlowEditor(
			() => 'p1',
			() => 'gone'
		);
		await ed.load();
		expect(ed.error).toBe('no such flow');
		expect(ed.flow).toBeNull();
	});

	it('loads the contracts and the schema beside the flow', async () => {
		const action = { slug: 'push' } as never;
		const signal = { slug: 'mr_merged' } as never;
		vi.mocked(api.system.actions).mockResolvedValue([action]);
		vi.mocked(api.system.signals).mockResolvedValue([signal]);
		const ed = await opened();
		await vi.waitFor(() => expect(ed.flowSchema).toEqual({ type: 'object' }));
		expect(ed.actions).toEqual([action]);
		expect(ed.signals).toEqual([signal]);
	});

	it('still opens without the contracts or the schema', async () => {
		vi.mocked(api.system.actions).mockRejectedValue(new Error('down'));
		flows.schema.mockRejectedValue(new Error('down'));
		const ed = await opened();
		expect(ed.error).toBe('');
		expect(ed.doc).not.toBeNull();
		expect(ed.flowSchema).toBeUndefined();
	});
});

describe('changing the document', () => {
	it('marks the graph dirty, clears the note and redraws', async () => {
		const ed = await opened();
		ed.notice = { tone: 'info', text: 'x' };
		const rev = ed.rev;
		edit(ed);
		expect(ed.dirty).toBe(true);
		expect(ed.notice).toBeNull();
		expect(ed.rev).toBe(rev + 1);
	});

	it('is dirty when the text alone changed', async () => {
		const ed = await opened();
		ed.yaml = NEWER;
		expect(ed.dirty).toBe(true);
	});

	it('is clean with no flow', () => {
		const ed = new FlowEditor(
			() => 'p1',
			() => 'x'
		);
		ed.graphDirty = true;
		expect(ed.dirty).toBe(false);
	});

	it('makes room for a chain added in the inspector', async () => {
		const ed = await opened();
		ed.doc!.phases!.push({ id: 'w', kind: 'watcher', on: { signal: 'manual' } });
		ed.markChanged();
		expect(ed.layout['trigger:w']).toBeDefined();
	});

	it('applies a dragged connection', async () => {
		const ed = await opened();
		ed.onlink('a', 'on_fail', 'b');
		expect(ed.doc!.stages[0].on_fail).toEqual({ to: 'b', max: 3 });
		expect(ed.graphDirty).toBe(true);
	});

	it('changes nothing for a connection that means nothing', async () => {
		const ed = await opened();
		ed.onlink('nowhere', 'success', 'b');
		expect(ed.graphDirty).toBe(false);
		expect(ed.notice).toBeNull();
	});

	it('explains a connection across chains instead of applying it', async () => {
		const ed = await opened();
		ed.doc!.stages[1].phase = 'other';
		ed.onlink('a', 'success', 'b');
		expect(ed.notice?.tone).toBe('info');
		expect(ed.notice?.text).toContain('a');
		expect(ed.graphDirty).toBe(false);
	});

	it('ignores a connection with no document', () => {
		const ed = new FlowEditor(
			() => 'p1',
			() => 'x'
		);
		expect(() => ed.onlink('a', 'success', 'b')).not.toThrow();
	});
});

describe('the tabs', () => {
	it('writes the document out on the way to the text, only when it changed', async () => {
		const ed = await opened();
		ed.switchTab('yaml');
		expect(ed.yaml).toBe(YAML);
		ed.switchTab('yaml');
		ed.tab = 'graph';
		edit(ed);
		ed.switchTab('yaml');
		expect(ed.tab).toBe('yaml');
		expect(ed.yaml).toContain('run: changed');
	});

	it('moves between graph and agents with nothing to read again', async () => {
		const ed = await opened();
		flows.validate.mockClear();
		ed.switchTab('agents');
		expect(ed.tab).toBe('agents');
		expect(flows.validate).not.toHaveBeenCalled();
	});

	it('reads the text again on the way back', async () => {
		const ed = await opened();
		ed.switchTab('yaml');
		ed.yaml = YAML.replace(
			'  - { id: b, type: command, run: b }',
			'  - { id: b, type: command, run: b }\n  - { id: c, type: command, run: c }'
		);
		ed.switchTab('graph');
		await vi.waitFor(() => expect(ed.tab).toBe('graph'));
		expect(ed.doc?.stages.map((s) => s.id)).toEqual(['a', 'b', 'c']);
		expect(ed.layout.c).toBeDefined();
		expect(ed.layout.c).not.toEqual(ed.layout.b);
		expect(ed.graphDirty).toBe(false);
		expect(ed.validation).toBeNull();
	});

	it('keeps a stage’s place when the text comes back', async () => {
		const ed = await opened();
		ed.layout.a = { x: 500, y: 500 };
		ed.switchTab('yaml');
		ed.switchTab('graph');
		await vi.waitFor(() => expect(ed.tab).toBe('graph'));
		expect(ed.layout.a).toEqual({ x: 500, y: 500 });
	});

	it('stays on text that does not parse, saying why', async () => {
		const ed = await opened();
		ed.switchTab('yaml');
		flows.validate.mockResolvedValue(refused(['bad']));
		ed.switchTab('graph');
		await vi.waitFor(() => expect(ed.validateOpen).toBe(true));
		expect(ed.tab).toBe('yaml');
		expect(ed.validation).toEqual({
			ok: false,
			title: 'Fix the YAML to see the graph',
			errors: ['bad']
		});
	});

	it('says something for a refusal with no errors', async () => {
		const ed = await opened();
		ed.switchTab('yaml');
		flows.validate.mockResolvedValue(refused([]));
		ed.switchTab('graph');
		await vi.waitFor(() => expect(ed.validateOpen).toBe(true));
		expect(ed.validation?.errors).toEqual(['invalid flow']);
	});

	it('says so when the check itself failed', async () => {
		const ed = await opened();
		ed.switchTab('yaml');
		flows.validate.mockRejectedValue(new Error('down'));
		ed.switchTab('graph');
		await vi.waitFor(() => expect(ed.validateOpen).toBe(true));
		expect(ed.validation?.title).toBe('Validation failed');
		expect(ed.tab).toBe('yaml');
	});

	it('opens an agent in the Agents tab', async () => {
		const ed = await opened();
		ed.openAgent('worker');
		expect(ed.agentOpen).toBe('worker');
		expect(ed.tab).toBe('agents');
	});
});

describe('validate', () => {
	it('checks what would be saved: the document, from the graph', async () => {
		const ed = await opened();
		edit(ed);
		await ed.validate();
		expect(flows.validate).toHaveBeenLastCalledWith(docToYaml(ed.doc!));
	});

	it('checks the text, from the text', async () => {
		const ed = await opened();
		ed.switchTab('yaml');
		ed.yaml = 'text as typed';
		await ed.validate();
		expect(flows.validate).toHaveBeenLastCalledWith('text as typed');
	});

	it('reports a valid flow, with a toast', async () => {
		const ed = await opened();
		await ed.validate();
		expect(ed.validation).toMatchObject({ ok: true, title: 'Valid', stages: 2 });
		expect(ed.validateOpen).toBe(true);
		expect(ed.validating).toBe(false);
		expect(toast).toHaveBeenCalledWith('Valid · 2 stages', { tone: 'ok' });
	});

	it('warns of what is valid with warnings', async () => {
		const ed = await opened();
		flows.validate.mockResolvedValue(valid(YAML, { warnings: ['a', 'b'], stages: 1 }));
		await ed.validate();
		expect(ed.validation?.title).toBe('Valid, with binding warnings');
		expect(toast).toHaveBeenCalledWith('Valid · 1 stage, 2 binding warnings', { tone: 'warn' });
	});

	it('reports an invalid flow without a toast', async () => {
		const ed = await opened();
		vi.mocked(toast).mockClear();
		flows.validate.mockResolvedValue(refused(['bad']));
		await ed.validate();
		expect(ed.validation).toEqual({ ok: false, title: 'Invalid', errors: ['bad'] });
		expect(toast).not.toHaveBeenCalled();
	});

	it('reports a check that failed', async () => {
		const ed = await opened();
		flows.validate.mockRejectedValue(new Error('down'));
		await ed.validate();
		expect(ed.validation).toEqual({ ok: false, title: 'Validation failed', errors: ['down'] });
		expect(ed.validating).toBe(false);
		expect(ed.validateOpen).toBe(true);
	});

	it('is busy while it checks', async () => {
		const ed = await opened();
		const check = deferred<FlowValidation>();
		flows.validate.mockReturnValue(check.promise);
		const running = ed.validate();
		expect(ed.validating).toBe(true);
		check.resolve(valid());
		await running;
		expect(ed.validating).toBe(false);
	});
});

describe('save', () => {
	it('saves the document as YAML with the layout, and is clean after', async () => {
		const ed = await opened();
		edit(ed);
		const yaml = docToYaml(ed.doc!);
		flows.save.mockResolvedValue(savedFlow({ yaml }));
		await ed.save();
		expect(flows.save).toHaveBeenCalledWith('p1', 'builder', yaml, { nodes: ed.layout });
		expect(ed.yaml).toBe(yaml);
		expect(ed.dirty).toBe(false);
		expect(ed.busy).toBe(false);
		expect(ed.validation).toBeNull();
		expect(toast).toHaveBeenCalledWith('Saved · @bbbbbbbb — new runs start on this version');
	});

	it('saves the text as typed from the YAML tab', async () => {
		const ed = await opened();
		ed.switchTab('yaml');
		ed.yaml = NEWER;
		flows.save.mockResolvedValue(savedFlow());
		await ed.save();
		expect(flows.save.mock.calls[0][2]).toBe(NEWER);
	});

	it('saves the graph’s changes even from the YAML tab, when they are newer', async () => {
		const ed = await opened();
		edit(ed);
		ed.tab = 'yaml';
		flows.save.mockResolvedValue(savedFlow());
		await ed.save();
		expect(flows.save.mock.calls[0][2]).toContain('run: changed');
	});

	it('reads the saved document back and the history', async () => {
		const ed = await opened();
		edit(ed);
		const versions: FlowVersion[] = [{ hash: 'b', name: 'builder', yaml: NEWER, createdAt: '' }];
		flows.versions.mockResolvedValue(versions);
		flows.save.mockResolvedValue(savedFlow());
		await ed.save();
		expect(flows.validate).toHaveBeenLastCalledWith(NEWER);
		expect(ed.doc?.stages[0].run).toBe('changed');
		expect(ed.versions).toEqual(versions);
	});

	it('names a copy saved from a template', async () => {
		const ed = await opened(aFlowRow({ builtin: true }));
		edit(ed);
		flows.save.mockResolvedValue(savedFlow());
		await ed.save();
		expect(toast).toHaveBeenCalledWith('Saved as this workshop’s own copy · @bbbbbbbb');
	});

	it('keeps the warnings a save came back with in view', async () => {
		const ed = await opened();
		edit(ed);
		flows.save.mockResolvedValue(savedFlow());
		flows.validate.mockResolvedValue(valid(NEWER, { warnings: ['w'], stages: 2 }));
		await ed.save();
		expect(ed.validation).toEqual({
			ok: true,
			title: 'Saved, with binding warnings',
			stages: 2,
			hash: 'bbbbbbbb22222222',
			warnings: ['w']
		});
	});

	it('shows a refusal and stays dirty', async () => {
		const ed = await opened();
		edit(ed);
		flows.save.mockRejectedValue(new Error('invalid: line 2'));
		await ed.save();
		expect(ed.validation).toEqual({
			ok: false,
			title: 'Save refused',
			errors: ['invalid: line 2']
		});
		expect(ed.validateOpen).toBe(true);
		expect(ed.dirty).toBe(true);
		expect(ed.busy).toBe(false);
	});

	it.each<[string, () => FlowEditor | Promise<FlowEditor>]>([
		['clean', () => opened()],
		[
			'empty',
			() =>
				new FlowEditor(
					() => 'p1',
					() => 'x'
				)
		]
	])('does nothing when there is nothing to save: %s', async (_, make) => {
		const ed = await make();
		await ed.save();
		expect(flows.save).not.toHaveBeenCalled();
	});

	it('saves once while busy', async () => {
		const ed = await opened();
		edit(ed);
		const pending = deferred<Flow>();
		flows.save.mockReturnValue(pending.promise);
		const first = ed.save();
		expect(ed.busy).toBe(true);
		await ed.save();
		expect(flows.save).toHaveBeenCalledOnce();
		pending.resolve(savedFlow());
		await first;
	});

	// The versions fetch and the validate after a save only read it back: when
	// either fails, the flow is still saved and the toast has said so.
	it('does not call a save refused once the engine took it', async () => {
		const ed = await opened();
		edit(ed);
		flows.save.mockResolvedValue(savedFlow());
		flows.versions.mockRejectedValue(new Error('history is down'));
		await ed.save();
		expect(ed.validation?.title).not.toBe('Save refused');
	});

	it('finishes a save the engine took when reading it back fails', async () => {
		const ed = await opened();
		edit(ed);
		flows.save.mockResolvedValue(savedFlow());
		flows.validate.mockRejectedValue(new Error('parser is down'));
		await ed.save();
		expect(ed.validation).toBeNull();
		expect(ed.dirty).toBe(false);
		expect(ed.flow?.hash).toBe('bbbbbbbb22222222');
		expect(ed.doc).not.toBeNull();
	});
});

describe('the layout autosave', () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	const settle = () => vi.advanceTimersByTimeAsync(0);
	/** Run a drag's debounce out, leaving its save in flight. */
	const dragged = async (ed: FlowEditor, pending: Promise<Flow>) => {
		flows.save.mockReturnValueOnce(pending);
		ed.onmove('a', 10, 20);
		await vi.advanceTimersByTimeAsync(800);
	};

	it('saves a move once it settles: same text, new positions', async () => {
		const ed = await opened();
		flows.save.mockResolvedValue(aFlowRow());
		ed.onmove('a', 10, 20);
		expect(ed.layout.a).toEqual({ x: 10, y: 20 });
		await vi.advanceTimersByTimeAsync(799);
		expect(flows.save).not.toHaveBeenCalled();
		await vi.advanceTimersByTimeAsync(1);
		expect(flows.save).toHaveBeenCalledOnce();
		const [project, name, yaml, layout] = flows.save.mock.calls[0];
		expect([project, name, yaml]).toEqual(['p1', 'builder', YAML]);
		expect(layout?.nodes.a).toEqual({ x: 10, y: 20 });
	});

	it('saves a drag’s last position, once', async () => {
		const ed = await opened();
		flows.save.mockResolvedValue(aFlowRow());
		ed.onmove('a', 1, 1);
		await vi.advanceTimersByTimeAsync(500);
		ed.onmove('a', 2, 2);
		await vi.advanceTimersByTimeAsync(500);
		ed.onmove('a', 3, 3);
		await vi.advanceTimersByTimeAsync(800);
		expect(flows.save).toHaveBeenCalledOnce();
		expect(flows.save.mock.calls[0][3]?.nodes.a).toEqual({ x: 3, y: 3 });
	});

	it('takes the answer as the flow', async () => {
		const ed = await opened();
		flows.save.mockResolvedValue(aFlowRow({ updatedAt: 'later' }));
		ed.onmove('a', 1, 1);
		await vi.advanceTimersByTimeAsync(800);
		expect(ed.flow?.updatedAt).toBe('later');
	});

	it('waits for the explicit save while real edits are pending', async () => {
		const ed = await opened();
		edit(ed);
		ed.onmove('a', 1, 1);
		await vi.advanceTimersByTimeAsync(800);
		expect(flows.save).not.toHaveBeenCalled();
	});

	it('never saves a template: moving nodes is not adopting it', async () => {
		const ed = await opened(aFlowRow({ builtin: true }));
		ed.onmove('a', 1, 1);
		await vi.advanceTimersByTimeAsync(800);
		expect(flows.save).not.toHaveBeenCalled();
	});

	it('swallows a failed autosave: the next save carries it', async () => {
		const ed = await opened();
		flows.save.mockRejectedValue(new Error('down'));
		ed.onmove('a', 1, 1);
		await vi.advanceTimersByTimeAsync(800);
		expect(ed.error).toBe('');
		expect(ed.validation).toBeNull();
		expect(ed.flow?.yaml).toBe(YAML);
	});

	it('hands a pending move to the explicit save, and sends nothing after it', async () => {
		const ed = await opened();
		edit(ed);
		ed.onmove('a', 9, 9);
		flows.save.mockResolvedValue(savedFlow());
		await ed.save();
		await vi.advanceTimersByTimeAsync(5000);
		expect(flows.save).toHaveBeenCalledOnce();
		expect(flows.save.mock.calls[0][3]?.nodes.a).toEqual({ x: 9, y: 9 });
	});

	describe('racing an explicit save', () => {
		it('keeps the explicit save when the autosave answers after it', async () => {
			const ed = await opened();
			const layoutSave = deferred<Flow>();
			await dragged(ed, layoutSave.promise);

			edit(ed);
			flows.save.mockResolvedValueOnce(savedFlow());
			await ed.save();
			expect(ed.flow?.yaml).toBe(NEWER);

			layoutSave.resolve(aFlowRow());
			await settle();

			expect(ed.flow?.yaml).toBe(NEWER);
			expect(ed.flow?.hash).toBe('bbbbbbbb22222222');
			expect(ed.dirty).toBe(false);
		});

		it('keeps the explicit save when the autosave answers in the middle of it', async () => {
			const ed = await opened();
			const layoutSave = deferred<Flow>();
			const explicit = deferred<Flow>();
			await dragged(ed, layoutSave.promise);

			edit(ed);
			flows.save.mockReturnValueOnce(explicit.promise);
			const saving = ed.save();

			layoutSave.resolve(aFlowRow());
			await settle();
			expect(ed.flow?.hash).toBe('aaaaaaaa11111111');

			explicit.resolve(savedFlow());
			await saving;
			expect(ed.flow?.yaml).toBe(NEWER);
			expect(ed.dirty).toBe(false);
		});

		it('leaves a newer explicit save alone when the autosave fails after it', async () => {
			const ed = await opened();
			const layoutSave = deferred<Flow>();
			await dragged(ed, layoutSave.promise);

			edit(ed);
			flows.save.mockResolvedValueOnce(savedFlow());
			await ed.save();

			layoutSave.reject(new Error('down'));
			await settle();

			expect(ed.flow?.yaml).toBe(NEWER);
			expect(ed.error).toBe('');
		});

		it('does not take the autosave’s old flow back after a refused save', async () => {
			const ed = await opened();
			const layoutSave = deferred<Flow>();
			await dragged(ed, layoutSave.promise);

			edit(ed);
			flows.save.mockRejectedValueOnce(new Error('refused'));
			await ed.save();

			layoutSave.resolve(aFlowRow({ updatedAt: 'old' }));
			await settle();

			expect(ed.dirty).toBe(true);
			expect(ed.flow?.updatedAt).toBe('');
		});

		it('saves the newer text on the next move, not what the autosave held', async () => {
			const ed = await opened();
			const layoutSave = deferred<Flow>();
			await dragged(ed, layoutSave.promise);

			edit(ed);
			flows.save.mockResolvedValueOnce(savedFlow());
			await ed.save();
			layoutSave.resolve(aFlowRow());
			await settle();

			flows.save.mockResolvedValueOnce(savedFlow({ updatedAt: 'after' }));
			ed.onmove('b', 5, 5);
			await vi.advanceTimersByTimeAsync(800);

			expect(flows.save).toHaveBeenCalledTimes(3);
			expect(flows.save.mock.calls[2][2]).toBe(NEWER);
			expect(ed.flow?.updatedAt).toBe('after');
		});

		it('lets only the last of two autosaves land, in either order', async () => {
			const ed = await opened();
			const first = deferred<Flow>();
			const second = deferred<Flow>();
			await dragged(ed, first.promise);
			flows.save.mockReturnValueOnce(second.promise);
			ed.onmove('a', 30, 30);
			await vi.advanceTimersByTimeAsync(800);

			second.resolve(aFlowRow({ updatedAt: 'second' }));
			await settle();
			first.resolve(aFlowRow({ updatedAt: 'first' }));
			await settle();
			expect(ed.flow?.updatedAt).toBe('second');
		});

		it('drops the first of two autosaves that answers first too', async () => {
			const ed = await opened();
			const first = deferred<Flow>();
			const second = deferred<Flow>();
			await dragged(ed, first.promise);
			flows.save.mockReturnValueOnce(second.promise);
			ed.onmove('a', 30, 30);
			await vi.advanceTimersByTimeAsync(800);

			first.resolve(aFlowRow({ updatedAt: 'first' }));
			await settle();
			expect(ed.flow?.updatedAt).toBe('');
			second.resolve(aFlowRow({ updatedAt: 'second' }));
			await settle();
			expect(ed.flow?.updatedAt).toBe('second');
		});

		it('sends no autosave while an explicit save holds the document', async () => {
			const ed = await opened();
			edit(ed);
			const explicit = deferred<Flow>();
			flows.save.mockReturnValueOnce(explicit.promise);
			const saving = ed.save();
			ed.onmove('a', 40, 40);
			await vi.advanceTimersByTimeAsync(800);
			expect(flows.save).toHaveBeenCalledOnce();
			explicit.resolve(savedFlow());
			await saving;
		});

		// The explicit save took its layout when it started, and the timer finds
		// the flow dirty while it runs: a node dropped during a slow save (over
		// 800ms) is saved after it, or it jumps back on the next load.
		it('saves a node moved during an explicit save, once that save is done', async () => {
			const ed = await opened();
			edit(ed);
			// What went over the wire, then: the layout is a live object here.
			const sent: Flow['layout'][] = [];
			const explicit = deferred<Flow>();
			flows.save.mockImplementation((_project, _name, _yaml, layout) => {
				sent.push(wireCopy(layout));
				return explicit.promise;
			});
			const saving = ed.save();

			ed.onmove('a', 40, 40);
			await vi.advanceTimersByTimeAsync(800);
			explicit.resolve(savedFlow());
			await saving;

			await vi.advanceTimersByTimeAsync(5000);
			expect(sent.at(-1)?.nodes.a).toEqual({ x: 40, y: 40 });
		});
	});
});

describe('placing the nodes', () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it('arranges with what was measured, shows it all, and saves it', async () => {
		const ed = await opened();
		ed.layout.a = { x: 999, y: 999 };
		const refit = ed.mainRefit;
		flows.save.mockResolvedValue(aFlowRow());
		ed.autoArrange({ a: { width: 400, height: 100 } });
		expect(ed.layout.a.x).toBe(0);
		expect(ed.layout.b.x).toBe(456);
		expect(ed.mainRefit).toBe(refit + 1);
		await vi.advanceTimersByTimeAsync(800);
		expect(flows.save).toHaveBeenCalledOnce();
	});

	it('does nothing to arrange without a document', () => {
		const ed = new FlowEditor(
			() => 'p1',
			() => 'x'
		);
		ed.autoArrange();
		expect(ed.layout).toEqual({});
	});

	it('places a flow opened without a layout again once measured, once, unsaved', async () => {
		const ed = await opened();
		const rev = ed.rev;
		ed.onmeasured({ a: { width: 400, height: 100 } });
		expect(ed.layout.b.x).toBe(456);
		expect(ed.rev).toBe(rev + 1);
		ed.onmeasured({ a: { width: 900, height: 100 } });
		expect(ed.layout.b.x).toBe(456);
		await vi.advanceTimersByTimeAsync(5000);
		expect(flows.save).not.toHaveBeenCalled();
	});

	it('leaves a stored layout alone when measured', async () => {
		const nodes = { a: { x: 7, y: 8 }, b: { x: 9, y: 9 } };
		const ed = await opened(aFlowRow({ layout: { nodes } }));
		ed.onmeasured({ a: { width: 400, height: 100 } });
		expect(ed.layout).toEqual(nodes);
	});
});

describe('clone', () => {
	it('creates a flow under the new name, with the layout, and goes there', async () => {
		const ed = await opened();
		flows.create.mockResolvedValue(aFlowRow({ name: 'copy' }));
		await ed.clone('copy');
		const [project, name, yaml, layout] = flows.create.mock.calls[0];
		expect([project, name]).toEqual(['p1', 'copy']);
		expect(yaml).toContain('name: copy\n');
		expect(yaml).not.toContain('name: builder');
		expect(layout).toEqual({ nodes: ed.layout });
		expect(ed.cloning).toBe(false);
		expect(toast).toHaveBeenCalledWith('Cloned as copy');
		expect(goto).toHaveBeenCalledWith('/w/p1/flows/copy');
	});

	it('renames the flow alone, not a `name:` deeper in the document', async () => {
		const ed = await opened();
		ed.doc!.agents = [{ name: 'builder', prompt: 'p' }];
		ed.markChanged();
		flows.create.mockResolvedValue(aFlowRow());
		await ed.clone('copy');
		expect(flows.create.mock.calls[0][2]).toContain('- name: builder');
	});

	it('does not leave when the clone is refused', async () => {
		const ed = await opened();
		ed.cloning = true;
		vi.mocked(goto).mockClear();
		flows.create.mockRejectedValue(new Error('taken'));
		await expect(ed.clone('copy')).rejects.toThrow('taken');
		expect(goto).not.toHaveBeenCalled();
		expect(ed.cloning).toBe(true);
	});

	it('does nothing with no flow', async () => {
		const ed = new FlowEditor(
			() => 'p1',
			() => 'x'
		);
		await ed.clone('copy');
		expect(flows.create).not.toHaveBeenCalled();
	});
});

describe('loading a version', () => {
	const v: FlowVersion = { hash: 'cccccccc33333333', name: 'builder', yaml: '', createdAt: '' };

	it('puts it in the text, saved by Save', async () => {
		const ed = await opened();
		ed.showVersions = true;
		flows.version.mockResolvedValue({ ...v, yaml: NEWER });
		await ed.loadVersion(v);
		expect(flows.version).toHaveBeenCalledWith('p1', v.hash);
		expect(ed.yaml).toBe(NEWER);
		expect(ed.tab).toBe('yaml');
		expect(ed.graphDirty).toBe(false);
		expect(ed.showVersions).toBe(false);
		expect(ed.notice?.text).toBe(
			'Loaded @cccccccc into the editor — Save to make it current again.'
		);
		expect(confirmDialog).not.toHaveBeenCalled();
	});

	it('asks before replacing unsaved work', async () => {
		const ed = await opened();
		edit(ed);
		vi.mocked(confirmDialog).mockResolvedValue(false);
		await ed.loadVersion(v);
		expect(flows.version).not.toHaveBeenCalled();
		vi.mocked(confirmDialog).mockResolvedValue(true);
		flows.version.mockResolvedValue({ ...v, yaml: NEWER });
		await ed.loadVersion(v);
		expect(ed.yaml).toBe(NEWER);
	});

	it('shows why a version would not load', async () => {
		const ed = await opened();
		flows.version.mockRejectedValue(new Error('gone'));
		await ed.loadVersion(v);
		expect(ed.error).toBe('gone');
		expect(ed.yaml).toBe(YAML);
	});
});

describe('remove', () => {
	it.each<[string, boolean, string, string]>([
		['deletes a flow', false, 'Delete builder?', 'builder deleted'],
		[
			'resets one that shadows a template',
			true,
			'Reset builder to built-in?',
			'builder reset to the built-in template'
		]
	])('%s', async (_, shadows, title, done) => {
		const ed = await opened(aFlowRow({ shadowsBuiltin: shadows }));
		vi.mocked(confirmDialog).mockResolvedValue(true);
		flows.remove.mockResolvedValue(undefined);
		await ed.remove();
		expect(vi.mocked(confirmDialog).mock.calls[0][0].title).toBe(title);
		expect(flows.remove).toHaveBeenCalledWith('p1', 'builder');
		expect(toast).toHaveBeenCalledWith(done);
		expect(goto).toHaveBeenCalledWith('/w/p1?tab=flows');
	});

	it('does nothing when it is told no', async () => {
		const ed = await opened();
		vi.mocked(confirmDialog).mockResolvedValue(false);
		await ed.remove();
		expect(flows.remove).not.toHaveBeenCalled();
	});

	it('shows why it could not, and stays', async () => {
		const ed = await opened();
		vi.mocked(confirmDialog).mockResolvedValue(true);
		vi.mocked(goto).mockClear();
		flows.remove.mockRejectedValue(new Error('in use'));
		await ed.remove();
		expect(ed.error).toBe('in use');
		expect(goto).not.toHaveBeenCalled();
	});
});

describe('leaving', () => {
	const leaving = (over: Partial<BeforeNavigate> = {}) =>
		({
			type: 'link',
			cancel: vi.fn(),
			to: { url: new URL('http://x.test/w') },
			...over
		}) as unknown as BeforeNavigate;

	it('lets a clean editor go', async () => {
		const ed = await opened();
		const nav = leaving();
		ed.beforeLeave(nav);
		expect(nav.cancel).not.toHaveBeenCalled();
		expect(confirmDialog).not.toHaveBeenCalled();
	});

	it('leaves closing the tab to the browser’s own prompt', async () => {
		const ed = await opened();
		edit(ed);
		const nav = leaving({ type: 'leave' });
		ed.beforeLeave(nav);
		expect(nav.cancel).toHaveBeenCalled();
		expect(confirmDialog).not.toHaveBeenCalled();
	});

	it('asks, and goes only when told to', async () => {
		const ed = await opened();
		edit(ed);
		vi.mocked(goto).mockClear();
		vi.mocked(confirmDialog).mockResolvedValue(false);
		const nav = leaving();
		ed.beforeLeave(nav);
		expect(nav.cancel).toHaveBeenCalled();
		await vi.waitFor(() => expect(confirmDialog).toHaveBeenCalled());
		expect(goto).not.toHaveBeenCalled();
	});

	it('goes where it was headed, once, past its own guard', async () => {
		const ed = await opened();
		edit(ed);
		vi.mocked(confirmDialog).mockResolvedValue(true);
		const nav = leaving();
		ed.beforeLeave(nav);
		await vi.waitFor(() => expect(goto).toHaveBeenCalledWith(nav.to?.url));
		const again = leaving();
		ed.beforeLeave(again);
		expect(again.cancel).not.toHaveBeenCalled();
	});

	it('lets its own clone leave, however dirty', async () => {
		const ed = await opened();
		edit(ed);
		flows.create.mockResolvedValue(aFlowRow());
		await ed.clone('copy');
		const nav = leaving();
		ed.beforeLeave(nav);
		expect(nav.cancel).not.toHaveBeenCalled();
	});
});
