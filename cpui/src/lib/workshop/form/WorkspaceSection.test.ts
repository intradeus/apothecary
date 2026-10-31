import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { bindable } from '$test/fixtures/app.svelte';
import WorkspaceSection from './WorkspaceSection.svelte';

vi.mock('$lib/api', () => ({ api: { egress: { defaults: vi.fn() } } }));

const setup = (over: Record<string, unknown> = {}) => {
	const props = bindable({
		blockedDomains: [] as string[],
		egressMode: 'allow' as 'allow' | 'deny',
		allowDomains: [] as string[],
		denyDomains: [] as string[],
		codeServerExtensions: [] as string[],
		editor: {
			theme: '',
			fontSize: '' as number | '',
			tabSize: '' as number | '',
			formatOnSave: false,
			wordWrap: false,
			trustWorkspace: false
		},
		onerror: vi.fn(),
		...over
	});
	render(WorkspaceSection, props);
	return props;
};

describe('WorkspaceSection', () => {
	it('binds the extensions and counts them', async () => {
		const props = setup();
		await userEvent.type(
			screen.getByPlaceholderText('golang.go, ms-python.python'),
			'golang.go{Enter}'
		);
		expect(props.codeServerExtensions).toEqual(['golang.go']);
		expect(
			screen.getByRole('heading', { name: 'code-server extensions' }).nextElementSibling
		).toHaveTextContent('1');
	});

	it('binds through to the egress policy', async () => {
		const props = setup();
		await userEvent.click(screen.getByRole('radio', { name: 'Denylist' }));
		expect(props.egressMode).toBe('deny');
		await userEvent.type(
			screen.getByPlaceholderText('tracker.example.com, *.ads.net'),
			'x.test{Enter}'
		);
		expect(props.denyDomains).toEqual(['x.test']);
	});

	it('binds through to the editor defaults', async () => {
		const props = setup();
		await userEvent.click(screen.getByRole('switch', { name: 'Word wrap' }));
		expect(props.editor.wordWrap).toBe(true);
	});

	it('passes a failed defaults fetch up', async () => {
		vi.mocked(api.egress.defaults).mockRejectedValue(new Error('down'));
		const props = setup();
		await userEvent.click(screen.getByRole('button', { name: /Add sensible defaults/ }));
		expect(props.onerror).toHaveBeenCalledWith('down');
	});
});
