// Flow documents for the editor's tests. A panel edits `doc` in place and
// reads it back as it renders, so a document here is deep $state, as the
// editor's is (`FlowEditor.doc`). Hence the .svelte.ts.
import { tick } from 'svelte';
import { screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { expect, vi } from 'vitest';
import type { ActionSpec, Flow, FlowVersion, ParamDecl, SignalSpec } from '$lib/domain';
import type {
	DocAgent,
	DocMCPServer,
	DocPhase,
	DocSequence,
	DocStage,
	FlowDoc,
	StageKind
} from '$lib/domain/flowdoc';
import { confirmState } from '$lib/ui/confirm.svelte';

/** Deep $state: mutations made by the panel show up in what it renders. */
export function live<T>(value: T): T {
	const state = $state(value);
	return state;
}

export const aStage = (id: string, type: StageKind = 'command', over: Partial<DocStage> = {}) =>
	({ id, type, phase: 'main', ...over }) as DocStage;

export const MAIN_PHASE: DocPhase = { id: 'main', kind: 'main', on: { signal: '' } };

export const aWatcher = (id = 'on-ci', over: Partial<DocPhase> = {}): DocPhase => ({
	id,
	kind: 'watcher',
	on: { signal: 'manual' },
	...over
});

export const anOutcome = (id = 'merged', over: Partial<DocPhase> = {}): DocPhase => ({
	id,
	kind: 'outcome',
	on: { signal: 'mr_merged' },
	then: 'done',
	...over
});

export const aSequence = (id = 'lint', stages: DocStage[] = [], over: Partial<DocSequence> = {}) =>
	({ id, stages, ...over }) as DocSequence;

export const anAgent = (name = 'planner', over: Partial<DocAgent> = {}): DocAgent => ({
	name,
	prompt: 'You plan.',
	...over
});

export const aServer = (name = 'tools', over: Partial<DocMCPServer> = {}): DocMCPServer => ({
	name,
	command: ['npx', 'tools'],
	...over
});

/** A one-stage flow; `over` replaces what it states. */
export const aDoc = (over: Partial<FlowDoc> = {}): FlowDoc =>
	live({
		name: 'builder',
		parameters: {},
		stages: [aStage('build', 'command', { on_success: 'done' })],
		phases: [{ ...MAIN_PHASE }],
		...over
	});

export const aSignal = (slug: string, over: Partial<SignalSpec> = {}): SignalSpec => ({
	slug,
	title: slug.replaceAll('_', ' '),
	kind: 'observed',
	debounced: false,
	all: false,
	...over
});

export const anAction = (slug: string, over: Partial<ActionSpec> = {}): ActionSpec => ({
	slug,
	title: slug.replaceAll('_', ' '),
	single: false,
	remote: false,
	inputs: [],
	outputs: [],
	settings: [],
	...over
});

export const aFlow = (over: Partial<Flow> = {}): Flow => ({
	projectId: 'p1',
	name: 'builder',
	yaml: 'name: builder\n',
	hash: 'abcdef0123456789',
	updatedAt: '2026-10-31T12:00:00Z',
	builtin: false,
	shadowsBuiltin: false,
	operatesOn: 'ticket',
	parameters: [],
	phases: [],
	...over
});

export const aVersion = (hash: string, over: Partial<FlowVersion> = {}): FlowVersion => ({
	hash,
	name: 'builder',
	yaml: 'name: builder\n',
	createdAt: '2026-10-31T11:00:00Z',
	...over
});

export const aParam = (name: string, over: Partial<ParamDecl> = {}): ParamDecl => ({
	name,
	type: 'string',
	default: '',
	known: false,
	...over
});

/** The callbacks every panel takes. */
export const handlers = () => ({
	onchange: vi.fn(),
	onselect: vi.fn(),
	onopen: vi.fn(),
	ondelete: vi.fn()
});

/** Opens a Select by its name and picks an option by its label. */
export async function pick(name: string | RegExp, option: string | RegExp) {
	await userEvent.click(screen.getByRole('combobox', { name }));
	await userEvent.click(await screen.findByRole('option', { name: option }));
}

/** The labels a Select offers; it is left closed. */
export async function optionsOf(name: string | RegExp) {
	const field = screen.getByRole('combobox', { name });
	await userEvent.click(field);
	const labels = (await screen.findAllByRole('option')).map((o) =>
		o.textContent?.replace('✓', '').trim()
	);
	await userEvent.keyboard('{Escape}');
	await vi.waitFor(() => expect(field).toHaveAttribute('aria-expanded', 'false'));
	return labels;
}

/** Replaces a field's text and leaves it: what commits a rename. */
export async function retype(el: HTMLElement, text: string) {
	await userEvent.clear(el);
	if (text) await userEvent.type(el, text);
	await userEvent.tab();
}

/** Replaces a field's text, keeping the focus: for fields that commit as typed. */
export async function fill(el: HTMLElement, text: string) {
	await userEvent.clear(el);
	if (text) await userEvent.type(el, text);
}

/** Waits for the confirm dialog, answers it, and says what it asked. */
export async function answer(ok: boolean) {
	await vi.waitFor(() => expect(confirmState.current).not.toBeNull());
	const asked = confirmState.current!;
	confirmState.settle(ok);
	await tick();
	await tick();
	return asked;
}
