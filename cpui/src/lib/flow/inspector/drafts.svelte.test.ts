import { flushSync } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { DocMCPServer, DocSchema } from '$lib/domain/flowdoc';
import { inEffectRoot, reactive } from '$test/fixtures/logic.svelte';
import { MCPDraft, SchemaDraft } from './drafts.svelte';

let stop: (() => void) | undefined;
afterEach(() => {
	stop?.();
	stop = undefined;
});

function schemaDraft(initial: DocSchema | undefined) {
	const box = reactive<{ q: DocSchema | undefined }>({ q: initial });
	const onchange = vi.fn();
	let draft!: SchemaDraft;
	stop = inEffectRoot(() => {
		draft = new SchemaDraft(() => box.q, onchange);
	});
	flushSync();
	return { box, draft, onchange };
}

function mcpDraft(initial: DocMCPServer | undefined) {
	const box = reactive<{ m: DocMCPServer | undefined }>({ m: initial });
	const onchange = vi.fn();
	let draft!: MCPDraft;
	stop = inEffectRoot(() => {
		draft = new MCPDraft(() => box.m, onchange);
	});
	flushSync();
	return { box, draft, onchange };
}

describe('SchemaDraft', () => {
	const schema = (): DocSchema => ({ name: 's', json_schema: { type: 'object' } });

	it('shows the body as YAML, and changes nothing by showing it', () => {
		const { draft, onchange } = schemaDraft(schema());
		expect(draft.text).toBe('type: object\n');
		expect(draft.error).toBe('');
		expect(onchange).not.toHaveBeenCalled();
	});

	it('takes what was typed once it parses', () => {
		const { box, draft, onchange } = schemaDraft(schema());
		draft.text = 'type: string\n';
		flushSync();
		expect(box.q?.json_schema).toEqual({ type: 'string' });
		expect(onchange).toHaveBeenCalledOnce();
	});

	it('holds a half-typed line back, saying why, the document untouched', () => {
		const { box, draft, onchange } = schemaDraft(schema());
		draft.text = 'type: [unclosed';
		flushSync();
		expect(draft.error).not.toBe('');
		expect(draft.error).not.toContain('\n');
		expect(box.q?.json_schema).toEqual({ type: 'object' });
		expect(onchange).not.toHaveBeenCalled();
	});

	it('clears the error once it parses again', () => {
		const { draft } = schemaDraft(schema());
		draft.text = 'type: [unclosed';
		flushSync();
		draft.text = 'type: object\n';
		flushSync();
		expect(draft.error).toBe('');
	});

	it('reads an emptied body as an empty schema', () => {
		const { box, draft } = schemaDraft(schema());
		draft.text = '';
		flushSync();
		expect(box.q?.json_schema).toEqual({});
	});

	it('changes nothing for text that says the same', () => {
		const { draft, onchange } = schemaDraft(schema());
		draft.text = '# a comment\ntype: object\n';
		flushSync();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('loads another schema when the selection moves, dropping the error', () => {
		const { box, draft } = schemaDraft(schema());
		draft.text = 'type: [unclosed';
		flushSync();
		box.q = { name: 't', json_schema: { type: 'array' } };
		flushSync();
		expect(draft.text).toBe('type: array\n');
		expect(draft.error).toBe('');
	});

	it('is empty with no schema, typing going nowhere', () => {
		const { draft, onchange } = schemaDraft(undefined);
		expect(draft.text).toBe('');
		draft.text = 'type: object';
		flushSync();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('shows a schema with no body as empty', () => {
		const { draft } = schemaDraft({ name: 's', json_schema: undefined });
		expect(draft.text).toBe('{}\n');
	});
});

describe('MCPDraft', () => {
	it('shows a command a word per line, its env as rows', () => {
		const { draft, onchange } = mcpDraft({
			name: 'm',
			command: ['npx', '-y', 'x'],
			env: { A: '1' }
		});
		expect(draft.command).toBe('npx\n-y\nx');
		expect(draft.rows).toEqual([{ key: 'A', value: '1' }]);
		expect(onchange).not.toHaveBeenCalled();
	});

	it('shows a url’s headers as rows', () => {
		const { draft } = mcpDraft({ name: 'm', url: 'u', headers: { H: 'v' }, env: { E: 'x' } });
		expect(draft.rows).toEqual([{ key: 'H', value: 'v' }]);
	});

	it('shows a builtin no rows', () => {
		const { draft } = mcpDraft({ name: 'm', builtin: 'browser', env: { E: 'x' } });
		expect(draft.rows).toEqual([]);
	});

	it('takes the command as it is typed, a word per line, blanks out', () => {
		const { box, draft, onchange } = mcpDraft({ name: 'm', command: ['a'] });
		draft.command = ' npx \n\n  -y\n';
		flushSync();
		expect(box.m?.command).toEqual(['npx', '-y']);
		expect(onchange).toHaveBeenCalledOnce();
	});

	it('changes nothing for a command that says the same', () => {
		const { draft, onchange } = mcpDraft({ name: 'm', command: ['a'] });
		draft.command = '  a  \n';
		flushSync();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('takes no command on a server that is not one', () => {
		const { box, draft, onchange } = mcpDraft({ name: 'm', url: 'u' });
		draft.command = 'x';
		flushSync();
		expect(box.m?.command).toBeUndefined();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('takes rows with a name as a command’s env', () => {
		const { box, draft, onchange } = mcpDraft({ name: 'm', command: ['a'] });
		draft.rows = [
			{ key: ' A ', value: '1' },
			{ key: '  ', value: 'dropped' }
		];
		flushSync();
		expect(box.m?.env).toEqual({ A: '1' });
		expect(onchange).toHaveBeenCalledOnce();
	});

	it('takes rows as a url’s headers', () => {
		const { box, draft } = mcpDraft({ name: 'm', url: 'u' });
		draft.rows = [{ key: 'H', value: 'v' }];
		flushSync();
		expect(box.m?.headers).toEqual({ H: 'v' });
		expect(box.m?.env).toBeUndefined();
	});

	it('removes the map with its last row', () => {
		const { box, draft } = mcpDraft({ name: 'm', command: ['a'], env: { A: '1' } });
		draft.rows = [];
		flushSync();
		expect(box.m?.env).toBeUndefined();
	});

	it('takes no rows on a builtin', () => {
		const { box, draft, onchange } = mcpDraft({ name: 'm', builtin: 'browser' });
		draft.rows = [{ key: 'A', value: '1' }];
		flushSync();
		expect(box.m?.env).toBeUndefined();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('loads another server when the selection moves', () => {
		const { box, draft } = mcpDraft({ name: 'm', command: ['a'] });
		box.m = { name: 'n', command: ['b', 'c'], env: { X: 'y' } };
		flushSync();
		expect(draft.command).toBe('b\nc');
		expect(draft.rows).toEqual([{ key: 'X', value: 'y' }]);
	});

	it('is empty with no server', () => {
		const { draft } = mcpDraft(undefined);
		expect(draft.command).toBe('');
		expect(draft.rows).toEqual([]);
	});
});
