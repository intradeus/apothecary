import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { RunCheckout } from '$lib/domain';
import { bound } from '$test/fixtures/run-components';
import { text } from '$test/helpers';
import RepoBar from './RepoBar.svelte';

const checkout = (name: string, primary = false) =>
	({
		name,
		primary,
		dir: name,
		url: `https://git.test/${name}.git`,
		branch: 'main'
	}) as RunCheckout;
const two = [checkout('api', true), checkout('docs')];

describe('RepoBar', () => {
	it('asks nothing of a single-repo run', () => {
		render(RepoBar, { checkouts: [checkout('api', true)] });

		expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
		expect(screen.queryByText('Repo')).not.toBeInTheDocument();
	});

	it('offers each checkout, the primary starred and selected by default', () => {
		render(RepoBar, { checkouts: two });

		expect(screen.getByText('Repo')).toBeInTheDocument();
		expect(screen.getByRole('radiogroup', { name: 'Repository' })).toBeInTheDocument();

		const primary = screen.getByRole('radio', { name: /^api / });
		expect(primary).toBeChecked();
		expect(primary).toHaveAttribute('title', 'https://git.test/api.git (primary)');

		const other = screen.getByRole('radio', { name: 'docs' });
		expect(other).not.toBeChecked();
		expect(other).toHaveAttribute('title', 'https://git.test/docs.git');
	});

	it('binds the checkout picked, and back to empty for the primary', async () => {
		const props = bound({ checkouts: two, value: '' });
		render(RepoBar, { props });

		await userEvent.click(screen.getByRole('radio', { name: 'docs' }));
		expect(props.value).toBe('docs');
		expect(screen.getByRole('radio', { name: 'docs' })).toBeChecked();

		await userEvent.click(screen.getByRole('radio', { name: /^api / }));
		expect(props.value).toBe('');
	});

	it('shows the hint and what it is given, even with one repo', () => {
		render(RepoBar, {
			checkouts: [checkout('api', true)],
			hint: 'read-only',
			children: text('extra control')
		});

		expect(screen.getByText('read-only')).toBeInTheDocument();
		expect(screen.getByText('extra control')).toBeInTheDocument();
	});
});
