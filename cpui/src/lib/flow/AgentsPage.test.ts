import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aDoc, anAgent, answer, aStage, fill, retype } from '$test/fixtures/flow.svelte';
import type { FlowDoc } from '$lib/domain/flowdoc';
import AgentsPage from './AgentsPage.svelte';

const setup = (over: Partial<FlowDoc> = {}, selected?: string) => {
	const doc = aDoc({ agents: [anAgent('planner'), anAgent('reviewer')], ...over });
	const h = { onchange: vi.fn(), onopenstage: vi.fn(), onopenmcp: vi.fn() };
	render(AgentsPage, { doc, selected, ...h });
	return { doc, ...h };
};
const list = () => within(screen.getByRole('navigation', { name: 'The flow’s agents' }));
const item = (name: RegExp) => list().getByRole('button', { name });
const open = () => screen.getAllByRole('textbox', { name: 'Name' })[0];

describe('AgentsPage', () => {
	it('opens on the first agent', () => {
		setup();
		expect(item(/^planner/)).toHaveAttribute('aria-current', 'true');
		expect(open()).toHaveValue('planner');
	});

	it('opens on the one asked for', () => {
		setup({}, 'reviewer');
		expect(item(/^reviewer/)).toHaveAttribute('aria-current', 'true');
		expect(open()).toHaveValue('reviewer');
	});

	it('falls back to the first when the one asked for is gone', () => {
		setup({}, 'gone');
		expect(open()).toHaveValue('planner');
	});

	it('moves to the agent clicked', async () => {
		setup();
		await userEvent.click(item(/^reviewer/));
		expect(open()).toHaveValue('reviewer');
		expect(item(/^planner/)).not.toHaveAttribute('aria-current');
	});

	it('sums an agent up in the list', () => {
		setup({
			agents: [
				anAgent('planner', {
					description: 'Plans',
					read_only: true,
					allow_bash: true,
					mcp: ['tools'],
					inputs: [{ name: 'a' }],
					outputs: [{ name: 'b' }, { name: 'c' }]
				})
			],
			stages: [aStage('plan', 'agent', { agent: 'planner' })]
		});
		const row = item(/^planner/);
		expect(row).toHaveTextContent('Plans');
		expect(row).toHaveTextContent('1 in · 2 out · Read-only · Allow shell · mcp: tools');
		expect(row).toHaveTextContent('run by 1 stage');
	});

	it('flags an agent nothing runs', () => {
		setup();
		expect(item(/^planner/)).toHaveTextContent('not run');
	});

	describe('adding', () => {
		it('adds one and opens it', async () => {
			const { doc, onchange } = setup();
			await userEvent.click(list().getByRole('button', { name: /agent$/ }));
			expect(doc.agents!.map((a) => a.name)).toEqual(['planner', 'reviewer', 'agent_1']);
			expect(onchange).toHaveBeenCalledOnce();
			expect(open()).toHaveValue('agent_1');
		});

		it('offers the first one on an empty flow', async () => {
			const { doc } = setup({ agents: undefined });
			expect(list().getByText('This flow carries no agent yet.')).toBeInTheDocument();
			const buttons = screen.getAllByRole('button', { name: /agent$/ });
			expect(buttons).toHaveLength(2);
			await userEvent.click(buttons[1]);
			expect(doc.agents).toHaveLength(1);
			expect(open()).toHaveValue('agent_1');
		});
	});

	describe('finding one', () => {
		const many = {
			agents: ['a', 'b', 'c', 'd']
				.map((n) => anAgent(n))
				.concat([anAgent('reviewer', { description: 'Reads diffs', prompt: 'Be KIND.' })])
		};
		const find = () => screen.getByPlaceholderText('Find an agent');

		it('offers a search only past four agents', () => {
			setup();
			expect(screen.queryByPlaceholderText('Find an agent')).not.toBeInTheDocument();
		});

		it.each([
			['name', 'reviewer'],
			['description, in any case', 'DIFFS'],
			['prompt, in any case', 'kind']
		])('finds by %s', async (_, query) => {
			setup(many);
			await fill(find(), query);
			expect(item(/^reviewer/)).toBeInTheDocument();
			expect(list().queryByRole('button', { name: /^a/ })).not.toBeInTheDocument();
		});

		it('says when nothing matches', async () => {
			setup(many);
			await fill(find(), 'nobody');
			expect(screen.getByText('No agent matches.')).toBeInTheDocument();
		});

		it('keeps the open agent open while it is filtered out', async () => {
			setup(many);
			await fill(find(), 'reviewer');
			expect(open()).toHaveValue('a');
		});
	});

	it('follows a rename of the first agent', async () => {
		setup();
		await retype(open(), 'architect');
		expect(item(/^architect/)).toHaveAttribute('aria-current', 'true');
		expect(open()).toHaveValue('architect');
	});

	it('stays on an agent renamed further down the list', async () => {
		setup({}, 'reviewer');
		await retype(open(), 'critic');
		expect(open()).toHaveValue('critic');
		expect(item(/^critic/)).toHaveAttribute('aria-current', 'true');
	});

	it('goes back to an agent when the open one is deleted', async () => {
		const { doc } = setup({}, 'reviewer');
		await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
		await answer(true);
		await vi.waitFor(() => expect(doc.agents).toHaveLength(1));
		await vi.waitFor(() => expect(open()).toHaveValue('planner'));
	});

	it('shows the empty page when the last one is deleted', async () => {
		setup({ agents: [anAgent('only')] });
		await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
		await answer(true);
		await vi.waitFor(() =>
			expect(screen.queryByRole('textbox', { name: 'Name' })).not.toBeInTheDocument()
		);
	});

	it('passes a stage or a server picked in the editor up to the page', async () => {
		const { onopenstage, onopenmcp } = setup({
			stages: [aStage('plan', 'agent', { agent: 'planner' })],
			mcp_servers: [{ name: 'tools', command: [] }]
		});
		await userEvent.click(screen.getByRole('button', { name: 'plan' }));
		expect(onopenstage).toHaveBeenCalledExactlyOnceWith({ label: 'plan', id: 'plan' });
		await userEvent.click(screen.getByRole('button', { name: 'Edit' }));
		expect(onopenmcp).toHaveBeenCalledExactlyOnceWith('tools');
	});
});
