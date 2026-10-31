import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import {
	aDoc,
	anAction,
	aStage,
	fill,
	handlers,
	optionsOf,
	pick,
	retype
} from '$test/fixtures/flow.svelte';
import type { ActionSpec } from '$lib/domain';
import type { DocStage, FlowDoc } from '$lib/domain/flowdoc';
import ActionStage from './ActionStage.svelte';

const commit = anAction('commit', {
	title: 'Commit',
	description: 'Commits the workspace.',
	inputs: [
		{ name: 'message', type: 'commit_message', required: true, defaultPath: 'artifacts/msg.txt' },
		{ name: 'notes', type: 'file', required: false }
	],
	outputs: [
		{ name: 'sha', type: 'file', required: true },
		{ name: 'log', type: 'file', required: true, defaultPath: 'artifacts/commit.log' }
	]
});
const label = anAction('label_mr', {
	title: 'Label',
	settings: [{ name: 'labels', kind: 'list', required: false }]
});
const sync = anAction('sync', {
	title: 'Sync',
	settings: [
		{ name: 'strategy', kind: 'enum', required: false, values: ['merge', 'rebase'] },
		{ name: 'status', kind: 'text', required: true, description: 'As the tracker spells it.' }
	]
});

const setup = (
	over: Partial<DocStage> = {},
	doc: Partial<FlowDoc> = {},
	actions: ActionSpec[] = [commit, label, sync]
) => {
	const d = aDoc({ stages: [aStage('act', 'action', over)], ...doc });
	const { onchange } = handlers();
	render(ActionStage, { doc: d, stage: d.stages[0], actions, onchange });
	return { stage: d.stages[0], onchange };
};

describe('ActionStage', () => {
	it('prefills a new action with its conventional paths', async () => {
		const { stage, onchange } = setup();
		await pick('Action', 'Commit');
		expect(stage.action).toBe('commit');
		// An input with no conventional path waits to be bound; an output gets one.
		expect(stage.input).toEqual({
			message: { from: 'artifacts/msg.txt', type: 'commit_message' }
		});
		expect(stage.output).toEqual({
			sha: { to: 'artifacts/sha', type: 'file' },
			log: { to: 'artifacts/commit.log', type: 'file' }
		});
		expect(onchange).toHaveBeenCalledOnce();
	});

	it('drops the old action’s ports and settings for the new one’s', async () => {
		const { stage } = setup({
			action: 'sync',
			input: { old: { from: 'artifacts/old' } },
			with: { status: 'Done' }
		});
		await pick('Action', 'Label');
		expect(stage.input).toBeUndefined();
		expect(stage.output).toBeUndefined();
		expect(stage.with).toBeUndefined();
	});

	it('takes everything off with the empty choice', async () => {
		const { stage } = setup({ action: 'commit', output: { sha: { to: 'x' } } });
		await pick('Action', '—');
		expect(stage.action).toBeUndefined();
		expect(stage.output).toBeUndefined();
	});

	it('warns of an action the engine does not have', () => {
		setup({ action: 'gone' });
		expect(screen.getByText('Unknown action “gone”.')).toBeInTheDocument();
	});

	it('shows the signature’s conditions', () => {
		const strict = anAction('strict', {
			title: 'Strict',
			description: 'Does it.',
			requires: 'repository',
			needs: 'merge_request',
			exactlyOne: ['to', 'status']
		});
		setup({ action: 'strict' }, {}, [strict]);
		expect(screen.getByText('Does it.')).toBeInTheDocument();
		expect(screen.getByText(/integration on the project running this flow/)).toBeInTheDocument();
		expect(screen.getByText(/Needs a merge request/)).toBeInTheDocument();
		expect(screen.getByText('Bind exactly one of to / status.')).toBeInTheDocument();
	});

	it('explains a tracker action instead of asking for an integration', () => {
		setup({ action: 't' }, {}, [anAction('t', { requires: 'tracker' })]);
		expect(screen.getByText(/Acts on the ticket's tracker issue/)).toBeInTheDocument();
		expect(screen.queryByText(/integration on the project/)).not.toBeInTheDocument();
	});

	it('wires the declared ports', async () => {
		const { stage, onchange } = setup({
			action: 'commit',
			input: { message: { from: 'artifacts/msg.txt', type: 'commit_message' } }
		});
		const from = screen.getAllByRole('combobox', { name: /From/ });
		await fill(from[1], 'artifacts/notes.md');
		expect(stage.input?.notes).toEqual({ from: 'artifacts/notes.md', type: 'file' });
		expect(onchange).toHaveBeenCalled();
	});

	describe('settings', () => {
		it('writes a text setting as typed, trimmed', async () => {
			const { stage, onchange } = setup({ action: 'sync' });
			await fill(screen.getByLabelText(/status/), ' Done ');
			expect(stage.with).toEqual({ status: 'Done' });
			expect(onchange).toHaveBeenCalled();
		});

		it('removes `with` along with its last setting', async () => {
			const { stage } = setup({ action: 'sync', with: { status: 'Done' } });
			await fill(screen.getByLabelText(/status/), '');
			expect(stage.with).toBeUndefined();
		});

		it('keeps the other settings when one changes', async () => {
			const { stage } = setup({ action: 'sync', with: { status: 'Done' } });
			await pick(/strategy/, 'rebase');
			expect(stage.with).toEqual({ status: 'Done', strategy: 'rebase' });
		});

		it('marks what is required', () => {
			setup({ action: 'sync' });
			expect(screen.getByLabelText(/status/).closest('.field')).toHaveTextContent('required');
			expect(screen.getByText('As the tracker spells it.')).toBeInTheDocument();
		});

		it('offers an enum parameter only when its values fit', async () => {
			setup(
				{ action: 'sync' },
				{
					parameters: {
						fits: { type: 'string', enum: ['merge'] },
						wider: { type: 'string', enum: ['merge', 'squash'] }
					}
				}
			);
			expect(await optionsOf(/strategy/)).toEqual(['—', 'merge', 'rebase', '$fits']);
		});

		it('keeps a parameter it reads that no longer fits', async () => {
			setup({ action: 'sync', with: { strategy: '$gone' } });
			expect(await optionsOf(/strategy/)).toContain('$gone');
		});

		it('writes a list as its items', async () => {
			const { stage } = setup({ action: 'label_mr' });
			await retype(screen.getByRole('textbox', { name: 'labels' }), 'bot, , ready');
			expect(stage.with).toEqual({ labels: ['bot', 'ready'] });
		});

		it('reads a list from a parameter, hiding the items', async () => {
			const { stage } = setup(
				{ action: 'label_mr', with: { labels: ['bot'] } },
				{ parameters: { tags: { type: 'string', list: true } } }
			);
			await pick(/labels/, '$tags');
			expect(stage.with).toEqual({ labels: '$tags' });
			expect(screen.queryByRole('textbox', { name: 'labels' })).not.toBeInTheDocument();
		});

		it('goes back to written items from a parameter', async () => {
			const { stage } = setup(
				{ action: 'label_mr', with: { labels: '$tags' } },
				{ parameters: { tags: { type: 'string', list: true } } }
			);
			await pick(/labels/, 'written here');
			expect(stage.with).toBeUndefined();
			expect(screen.getByRole('textbox', { name: 'labels' })).toBeInTheDocument();
		});
	});
});
