import { describe, it, expect, vi, beforeEach, type Mock } from 'vitest';
import { api } from '$lib/api';
import { openRunEditor } from './editor';

vi.mock('$lib/api', () => ({ api: { runs: { openEditor: vi.fn() } } }));

describe('openRunEditor', () => {
	let tab: { location: { href: string }; close: Mock };
	beforeEach(() => {
		tab = { location: { href: '' }, close: vi.fn() };
		vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window);
	});

	it('sends the tab it opened to the editor', async () => {
		vi.mocked(api.runs.openEditor).mockResolvedValue('https://editor.test');
		await openRunEditor('r1');
		expect(api.runs.openEditor).toHaveBeenCalledWith('r1');
		expect(tab.location.href).toBe('https://editor.test');
		expect(tab.close).not.toHaveBeenCalled();
	});

	it('closes the tab and rethrows when the editor will not open', async () => {
		vi.mocked(api.runs.openEditor).mockRejectedValue(new Error('no code-server'));
		await expect(openRunEditor('r1')).rejects.toThrow('no code-server');
		expect(tab.close).toHaveBeenCalledOnce();
	});
});
