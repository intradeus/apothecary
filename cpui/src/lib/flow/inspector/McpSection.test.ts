import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import {
	aDoc,
	anAgent,
	aServer,
	fill,
	handlers,
	live,
	optionsOf,
	pick,
	retype
} from '$test/fixtures/flow.svelte';
import type { DocMCPServer, FlowDoc } from '$lib/domain/flowdoc';
import type { MCPDraft } from './drafts.svelte';
import McpSection from './McpSection.svelte';

const setup = (
	over: Partial<DocMCPServer> = {},
	rows: { key: string; value: string }[] = [],
	more: Partial<FlowDoc> = {}
) => {
	const server = { name: 'tools', ...over };
	const doc = aDoc({ mcp_servers: [server, aServer('taken')], ...more });
	const draft = live({ command: '', rows }) as unknown as MCPDraft;
	const h = handlers();
	render(McpSection, { doc, server: doc.mcp_servers![0], draft, ...h });
	return { doc, server: doc.mcp_servers![0], draft, ...h };
};
const name = () => screen.getByLabelText('Name');

describe('McpSection', () => {
	it('renames the server and every agent listing it', async () => {
		const { doc, onchange, onselect } = setup({}, [], {
			agents: [anAgent('a', { mcp: ['tools'] })]
		});
		await retype(name(), 'kit');
		expect(doc.mcp_servers![0].name).toBe('kit');
		expect(doc.agents![0].mcp).toEqual(['kit']);
		expect(onchange).toHaveBeenCalledOnce();
		expect(onselect).toHaveBeenCalledExactlyOnceWith({ kind: 'mcp', name: 'kit' }, 'rename');
	});

	it.each([
		['a taken name', 'taken'],
		['a bad name', '9lives'],
		['nothing', '']
	])('refuses %s and puts the name back', async (_, text) => {
		const { server, onchange } = setup();
		await retype(name(), text);
		expect(name()).toHaveValue('tools');
		expect(server.name).toBe('tools');
		expect(onchange).not.toHaveBeenCalled();
	});

	it('writes the description, and takes an empty one off', async () => {
		const { server, onchange } = setup();
		await fill(screen.getByLabelText('Description'), 'Search');
		expect(server.description).toBe('Search');
		await fill(screen.getByLabelText('Description'), '');
		expect(server.description).toBeUndefined();
		expect(onchange).toHaveBeenCalled();
	});

	it('deletes the server', async () => {
		const { ondelete } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
		expect(ondelete).toHaveBeenCalledExactlyOnceWith('tools');
	});

	describe('how it is reached', () => {
		it('reads as a command until it says otherwise', () => {
			setup();
			expect(screen.getByRole('combobox', { name: 'Reached as' })).toHaveTextContent(
				'command — a program on stdio'
			);
			expect(screen.getByLabelText('Command')).toBeInTheDocument();
		});

		it('moves to a builtin, the browser first, dropping the command', async () => {
			const { server, onchange } = setup({ command: ['npx'], env: { TOKEN: 'x' } });
			await pick('Reached as', /^builtin/);
			expect(server).toMatchObject({ builtin: 'browser' });
			expect(server.command).toBeUndefined();
			expect(server.env).toBeUndefined();
			expect(onchange).toHaveBeenCalledOnce();
			expect(screen.getByRole('combobox', { name: 'Builtin' })).toBeInTheDocument();
			expect(screen.queryByText('Environment')).not.toBeInTheDocument();
		});

		it('moves to a url, keeping no env', async () => {
			const { server } = setup({ command: ['npx'], env: { TOKEN: 'x' } });
			await pick('Reached as', /^url/);
			expect(server.url).toBe('');
			expect(server.env).toBeUndefined();
			expect(screen.getByText('Headers')).toBeInTheDocument();
		});

		it('moves to a command, keeping no headers', async () => {
			const { server } = setup({ url: 'https://x.test', headers: { A: 'b' } });
			await pick('Reached as', /^command/);
			expect(server.command).toEqual([]);
			expect(server.url).toBeUndefined();
			expect(server.headers).toBeUndefined();
			expect(screen.getByText('Environment')).toBeInTheDocument();
		});

		it('writes the url trimmed', async () => {
			const { server, onchange } = setup({ url: '' });
			await fill(screen.getByLabelText('URL'), ' https://mcp.test/ ');
			expect(server.url).toBe('https://mcp.test/');
			expect(onchange).toHaveBeenCalled();
		});

		it('keeps a builtin the editor does not know', async () => {
			setup({ builtin: 'future' });
			expect(await optionsOf('Builtin')).toContain('future');
		});

		it('types the command into the draft', async () => {
			const { draft } = setup();
			await fill(screen.getByLabelText('Command'), 'npx{Enter}tools');
			expect(draft.command).toBe('npx\ntools');
		});
	});

	describe('its env and headers', () => {
		it('adds a row to the draft', async () => {
			const { draft } = setup();
			await userEvent.click(screen.getByRole('button', { name: /variable/ }));
			expect(draft.rows).toEqual([{ key: '', value: '' }]);
		});

		const key = (i = 0) => screen.getAllByLabelText('Key')[i];

		it('marks a key that is not a variable name', () => {
			setup({}, [{ key: 'X-Token', value: 'a' }]);
			expect(key()).toHaveClass('invalid');
		});

		it('takes the same key as a header name', () => {
			setup({ url: 'https://x.test' }, [{ key: 'X-Token', value: 'a' }]);
			expect(key()).not.toHaveClass('invalid');
		});

		it('marks a header name with a space', () => {
			setup({ url: 'https://x.test' }, [{ key: 'X Token', value: 'a' }]);
			expect(key()).toHaveClass('invalid');
		});

		it('marks a key given twice', () => {
			setup({}, [
				{ key: 'TOKEN', value: 'a' },
				{ key: 'TOKEN', value: 'b' }
			]);
			expect(key(1)).toHaveClass('invalid');
		});

		it('takes good keys', () => {
			setup({}, [{ key: 'TOKEN', value: '${secrets.x}' }]);
			expect(key()).not.toHaveClass('invalid');
		});
	});

	it('opens the agents listing it', async () => {
		const { onopen } = setup({}, [], { agents: [anAgent('a', { mcp: ['tools'] })] });
		await userEvent.click(screen.getByRole('button', { name: 'a' }));
		expect(onopen).toHaveBeenCalledExactlyOnceWith({ kind: 'agent', name: 'a' });
	});

	it('says when no agent lists it', () => {
		setup();
		expect(screen.getByText(/No agent lists it yet/)).toBeInTheDocument();
	});
});
