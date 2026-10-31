import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { ExtraRepo } from '$lib/domain';
import { bindable } from '$test/fixtures/app.svelte';
import ExtraRepos from './ExtraRepos.svelte';

const repo = (over: Partial<ExtraRepo> = {}): ExtraRepo => ({
	name: 'api',
	url: 'https://git.test/api.git',
	locator: '12',
	defaultBranch: 'main',
	...over
});

describe('ExtraRepos', () => {
	it('shows no table with none', () => {
		render(ExtraRepos, { rows: [], onadd: vi.fn() });
		expect(screen.queryByRole('table')).not.toBeInTheDocument();
	});

	it('asks the section to add one', async () => {
		const onadd = vi.fn();
		render(ExtraRepos, { rows: [], onadd });
		await userEvent.click(screen.getByRole('button', { name: /Add repository/ }));
		expect(onadd).toHaveBeenCalledOnce();
	});

	it('edits a row in place, each field bound', async () => {
		const props = bindable({
			rows: [repo({ name: '', url: '', locator: '', defaultBranch: '' })],
			onadd: vi.fn()
		});
		render(ExtraRepos, props);

		await userEvent.type(screen.getByLabelText('checkout name'), 'web');
		await userEvent.type(screen.getByLabelText('repo url'), 'https://x.git');
		await userEvent.type(screen.getByLabelText('locator (empty = read-only)'), '7');
		await userEvent.type(screen.getByLabelText('default branch'), 'dev');

		expect(props.rows[0]).toEqual({
			name: 'web',
			url: 'https://x.git',
			locator: '7',
			defaultBranch: 'dev'
		});
	});

	it('requires a name and a URL, not a locator', () => {
		render(ExtraRepos, { rows: [repo()], onadd: vi.fn() });
		expect(screen.getByLabelText('checkout name')).toBeRequired();
		expect(screen.getByLabelText('repo url')).toBeRequired();
		expect(screen.getByLabelText('locator (empty = read-only)')).not.toBeRequired();
	});

	it('removes the row it is on', async () => {
		const props = bindable({ rows: [repo({ name: 'a' }), repo({ name: 'b' })], onadd: vi.fn() });
		render(ExtraRepos, props);
		await userEvent.click(screen.getByRole('button', { name: 'Remove a' }));
		expect(props.rows.map((r) => r.name)).toEqual(['b']);
	});

	it('calls an unnamed row a repository', () => {
		render(ExtraRepos, { rows: [repo({ name: '' })], onadd: vi.fn() });
		expect(screen.getByRole('button', { name: 'Remove repository' })).toBeInTheDocument();
	});
});
