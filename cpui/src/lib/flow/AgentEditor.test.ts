import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import {
	aDoc,
	anAgent,
	answer,
	aSequence,
	aServer,
	aStage,
	fill,
	pick,
	retype
} from '$test/fixtures/flow.svelte';
import type { DocAgent, FlowDoc } from '$lib/domain/flowdoc';
import AgentEditor from './AgentEditor.svelte';

const setup = (agent: Partial<DocAgent> = {}, over: Partial<FlowDoc> = {}) => {
	const doc = aDoc({ agents: [anAgent('planner', agent), anAgent('taken')], ...over });
	const h = {
		onchange: vi.fn(),
		onrenamed: vi.fn(),
		ondeleted: vi.fn(),
		onopenstage: vi.fn(),
		onopenmcp: vi.fn()
	};
	render(AgentEditor, { doc, agent: doc.agents![0], ...h });
	return { doc, agent: doc.agents![0], ...h };
};
const contract = () => within(screen.getByRole('region', { name: 'Contract' }));
const name = () => screen.getAllByRole('textbox', { name: 'Name' })[0];
const portNames = () => screen.getAllByRole('textbox', { name: 'Name' }).slice(1);
const adds = () => document.querySelector('pre')?.textContent;

describe('AgentEditor', () => {
	describe('its name', () => {
		it('renames it and every stage running it', async () => {
			const { doc, onchange, onrenamed } = setup(
				{},
				{
					stages: [aStage('plan', 'agent', { agent: 'planner' })]
				}
			);
			await retype(name(), 'architect');
			expect(doc.agents![0].name).toBe('architect');
			expect(doc.stages[0].agent).toBe('architect');
			expect(onchange).toHaveBeenCalledOnce();
			expect(onrenamed).toHaveBeenCalledExactlyOnceWith('architect');
		});

		it.each([
			['a taken name', 'taken'],
			['a bad name', 'Not Ok'],
			['nothing', '']
		])('refuses %s and puts the name back', async (_, text) => {
			const { agent, onchange, onrenamed } = setup();
			await retype(name(), text);
			expect(name()).toHaveValue('planner');
			expect(agent.name).toBe('planner');
			expect(onchange).not.toHaveBeenCalled();
			expect(onrenamed).not.toHaveBeenCalled();
		});
	});

	describe('deleting', () => {
		it('asks, removes it and says so', async () => {
			const { doc, onchange, ondeleted } = setup();
			await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
			expect((await answer(true)).title).toBe('Delete agent planner?');
			await vi.waitFor(() => expect(ondeleted).toHaveBeenCalledOnce());
			expect(doc.agents!.map((a) => a.name)).toEqual(['taken']);
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('names the stages that stop validating', async () => {
			setup({}, { stages: [aStage('plan', 'agent', { agent: 'planner' })] });
			await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
			expect((await answer(false)).body).toContain('plan');
		});

		it('keeps it on a no', async () => {
			const { doc, onchange, ondeleted } = setup();
			await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
			await answer(false);
			expect(doc.agents).toHaveLength(2);
			expect(onchange).not.toHaveBeenCalled();
			expect(ondeleted).not.toHaveBeenCalled();
		});
	});

	describe('its settings', () => {
		it('writes the description, and takes an empty one off', async () => {
			const { agent, onchange } = setup();
			const field = contract().getAllByLabelText('Description')[0];
			await fill(field, 'Plans');
			expect(agent.description).toBe('Plans');
			await fill(field, '');
			expect(agent.description).toBeUndefined();
			expect(onchange).toHaveBeenCalled();
		});

		it.each([
			['Read-only', 'read_only'],
			['Allow shell', 'allow_bash']
		] as const)('writes %s only while it is on', async (label, field) => {
			const { agent, onchange } = setup();
			const toggle = screen.getByRole('switch', { name: label });
			await userEvent.click(toggle);
			expect(agent[field]).toBe(true);
			await userEvent.click(toggle);
			expect(agent[field]).toBeUndefined();
			expect(onchange).toHaveBeenCalledTimes(2);
		});
	});

	describe('its prompt', () => {
		it('writes the prompt and counts it', async () => {
			const { agent, onchange } = setup({ prompt: '' });
			expect(screen.getByText('0 lines · 0 words')).toBeInTheDocument();
			await fill(screen.getByRole('textbox', { name: 'Prompt' }), 'You plan{Enter}and you check');
			expect(agent.prompt).toBe('You plan\nand you check');
			expect(onchange).toHaveBeenCalled();
			expect(screen.getByText('2 lines · 5 words')).toBeInTheDocument();
		});

		it('previews what a stage adds, per port', () => {
			setup({
				inputs: [
					{ name: 'ticket', type: 'ticket' },
					{ name: 'answers', type: 'ping_answers' },
					{ name: 'fb', type: 'failure' },
					{ name: 'v', type: 'verdict' },
					{ name: 'diff', type: 'changeset' },
					{ name: 'app', type: 'link' },
					{ name: 'shots', type: 'files' },
					{ name: 'plan', type: 'file' }
				],
				outputs: [
					{ name: 'out', type: 'file' },
					{ name: 'dir', type: 'files' },
					{ name: 'typed', schema: 'blueprint' },
					{ name: 'bare' }
				]
			});
			const text = adds();
			expect(text).toContain('# PING ANSWERS — what the task’s owner answered during the run');
			expect(text).toContain('# INPUT: fb — the failure the edge carried');
			expect(text).toContain('# INPUT: v — the verdict the edge carried');
			expect(text).toContain('# INPUT: diff — the changed files, listed (never the diff)');
			expect(text).toContain('# INPUT: app — the link');
			expect(text).toContain('# INPUT: shots — the directory’s path, its files to read');
			expect(text).toContain('# INPUT: plan — the file’s path and its content');
			expect(text).toContain('# OUTPUT CONTRACT');
			expect(text).toContain('- <path> — out: file');
			expect(text).toContain('- <path/> — dir: files');
			expect(text).toContain('- <path> — typed: blueprint');
			expect(text).toContain('- <path> — bare: file');
		});

		it('leaves the output contract out with no outputs', () => {
			setup();
			expect(adds()).not.toContain('OUTPUT CONTRACT');
		});
	});

	describe('its ports', () => {
		const ports = {
			inputs: [{ name: 'plan', type: 'review_threads' }],
			outputs: [{ name: 'out', type: 'file' }]
		};
		const schemas = [{ name: 'blueprint', json_schema: {} }];

		it('adds an input and an output', async () => {
			const { agent, onchange } = setup();
			await userEvent.click(screen.getByRole('button', { name: /input$/ }));
			await userEvent.click(screen.getByRole('button', { name: /output$/ }));
			expect(agent.inputs).toHaveLength(1);
			expect(agent.outputs).toHaveLength(1);
			expect(onchange).toHaveBeenCalledTimes(2);
		});

		it('removes one', async () => {
			const { agent, onchange } = setup(ports);
			await userEvent.click(contract().getAllByRole('button', { name: 'Remove' })[0]);
			expect(agent.inputs ?? []).toHaveLength(0);
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('renames one, and the binding of every stage running it', async () => {
			const { doc, agent, onchange } = setup(ports, {
				stages: [
					aStage('run', 'agent', { agent: 'planner', input: { plan: { from: 'artifacts/p' } } })
				]
			});
			await retype(portNames()[0], 'draft');
			expect(agent.inputs![0].name).toBe('draft');
			expect(doc.stages[0].input).toEqual({ draft: { from: 'artifacts/p' } });
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('refuses a name its side already has', async () => {
			const { agent, onchange } = setup({
				outputs: [{ name: 'a' }, { name: 'b' }]
			});
			await retype(portNames()[1], 'a');
			expect(portNames()[1]).toHaveValue('b');
			expect(agent.outputs![1].name).toBe('b');
			expect(onchange).not.toHaveBeenCalled();
		});

		it('writes a port’s type', async () => {
			const { agent, onchange } = setup({ inputs: [{ name: 'in', type: 'file' }] });
			await pick('Type', 'verdict');
			expect(agent.inputs![0].type).toBe('verdict');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('writes a description on each side', async () => {
			const { agent } = setup(ports);
			const fields = screen.getAllByPlaceholderText('what it is (the agent reads this)');
			await fill(fields[0], 'The plan');
			await fill(fields[1], 'What came out');
			expect(agent.inputs![0].description).toBe('The plan');
			expect(agent.outputs![0].description).toBe('What came out');
		});

		it('marks an input optional', async () => {
			const { agent } = setup(ports);
			await userEvent.click(screen.getByRole('switch', { name: 'optional' }));
			expect(agent.inputs![0].optional).toBe(true);
		});

		const typeOf = (i: number) => screen.getAllByRole('combobox', { name: 'Type' })[i];

		it('offers an input every type, an output a file’s only', async () => {
			setup(ports);
			await userEvent.click(typeOf(0));
			expect(await screen.findByRole('option', { name: 'changeset' })).toBeInTheDocument();
			await userEvent.keyboard('{Escape}');
			await vi.waitFor(() => expect(screen.queryByRole('option')).not.toBeInTheDocument());
			await userEvent.click(typeOf(1));
			expect(await screen.findByRole('option', { name: /^file — any file/ })).toBeInTheDocument();
			expect(screen.queryByRole('option', { name: 'changeset' })).not.toBeInTheDocument();
		});

		it('drops a schema from a type that holds none', async () => {
			const { agent } = setup(
				{ inputs: [{ name: 'in', type: 'review_threads', schema: 'blueprint' }] },
				{ schemas }
			);
			await userEvent.click(typeOf(0));
			await userEvent.click(await screen.findByRole('option', { name: 'file' }));
			expect(agent.inputs![0].type).toBe('file');
			expect(agent.inputs![0].schema).toBeUndefined();
		});

		it('picks a schema, and keeps one the flow no longer has', async () => {
			const { agent, onchange } = setup(
				{ inputs: [{ name: 'in', type: 'review_threads', schema: 'gone' }] },
				{ schemas }
			);
			const field = screen.getByRole('combobox', { name: 'Schema' });
			expect(field).toHaveTextContent('gone');
			await pick('Schema', 'blueprint');
			expect(agent.inputs![0].schema).toBe('blueprint');
			expect(onchange).toHaveBeenCalledOnce();
		});

		it('offers no schema on a file', () => {
			setup({ inputs: [{ name: 'in', type: 'file' }] }, { schemas });
			expect(screen.queryByRole('combobox', { name: 'Schema' })).not.toBeInTheDocument();
		});
	});

	describe('its MCP servers', () => {
		const flow = {
			mcp_servers: [
				aServer('tools', { description: 'Handy' }),
				aServer('pw', { command: undefined, builtin: 'browser' }),
				aServer('remote', { command: undefined, url: 'https://x.test' }),
				aServer('run', { command: ['npx', 'run'] })
			]
		};

		it('turns a server on and off, taking the list off with its last', async () => {
			const { agent, onchange } = setup({}, flow);
			const toggle = screen.getByRole('switch', { name: 'tools' });
			await userEvent.click(toggle);
			expect(agent.mcp).toEqual(['tools']);
			await userEvent.click(toggle);
			expect(agent.mcp).toBeUndefined();
			expect(onchange).toHaveBeenCalledTimes(2);
		});

		it('says what each server is', () => {
			setup({}, flow);
			expect(screen.getByText('Handy')).toBeInTheDocument();
			expect(screen.getByText('browser — Chromium, through Playwright')).toBeInTheDocument();
			expect(screen.getByText('https://x.test')).toBeInTheDocument();
			expect(screen.getByText('npx run')).toBeInTheDocument();
		});

		it('opens one to edit it', async () => {
			const { onopenmcp } = setup({}, flow);
			await userEvent.click(screen.getAllByRole('button', { name: 'Edit' })[0]);
			expect(onopenmcp).toHaveBeenCalledExactlyOnceWith('tools');
		});

		it('adds a new server, listed and opened', async () => {
			const { doc, agent, onchange, onopenmcp } = setup();
			await userEvent.click(screen.getByRole('button', { name: /server$/ }));
			expect(doc.mcp_servers?.map((m) => m.name)).toEqual(['browser']);
			expect(agent.mcp).toEqual(['browser']);
			expect(onchange).toHaveBeenCalledOnce();
			expect(onopenmcp).toHaveBeenCalledExactlyOnceWith('browser');
		});

		it('shows one the flow does not declare, so it can be dropped', async () => {
			const { agent } = setup({ mcp: ['gone'] });
			expect(screen.getByText('not declared in this flow')).toBeInTheDocument();
			await userEvent.click(contract().getByRole('button', { name: 'Remove' }));
			expect(agent.mcp).toBeUndefined();
		});
	});

	describe('who runs it', () => {
		it('opens a stage running it, in a chain or a sequence', async () => {
			const { onopenstage } = setup(
				{},
				{
					stages: [aStage('plan', 'agent', { agent: 'planner' })],
					sequences: [aSequence('lint', [aStage('check', 'agent', { agent: 'planner' })])]
				}
			);
			expect(screen.getByText('run by 2 stages')).toBeInTheDocument();
			await userEvent.click(screen.getByRole('button', { name: 'lint › check' }));
			expect(onopenstage).toHaveBeenCalledExactlyOnceWith({
				label: 'lint › check',
				id: 'check',
				sequence: 'lint'
			});
		});

		it('says when nothing does', () => {
			setup();
			expect(screen.getByText(/No stage runs it yet/)).toBeInTheDocument();
		});
	});
});
