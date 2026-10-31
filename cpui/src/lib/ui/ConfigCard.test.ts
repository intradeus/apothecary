import { render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import ConfigCard from './ConfigCard.svelte';

const value = (v: string) => screen.getByText(v);

describe('ConfigCard', () => {
	it('names the thing and its kind', () => {
		render(ConfigCard, { title: 'acme-gitlab', kind: 'GitLab' });
		const card = within(screen.getByRole('article'));
		expect(card.getByText('acme-gitlab')).toBeInTheDocument();
		expect(card.getByText('GitLab')).toBeInTheDocument();
	});

	it('draws the glyph tile in its hue', () => {
		render(ConfigCard, { title: 'Ops', glyph: '#', hue: 'accent' });
		expect(screen.getByText('#')).toHaveClass('tone-accent');
		expect(screen.getByText('#')).toHaveAttribute('aria-hidden', 'true');
	});

	it.each([
		['a string', 'used by api, web'],
		['a snippet', text('used by api, web')]
	])('takes the quiet line as %s', (_, sub) => {
		render(ConfigCard, { title: 'x', sub });
		expect(screen.getByText('used by api, web')).toBeInTheDocument();
	});

	it('renders the status and actions it is given', () => {
		render(ConfigCard, { title: 'x', status: text('✓ ok'), actions: text('Edit') });
		expect(screen.getByText('✓ ok')).toBeInTheDocument();
		expect(screen.getByText('Edit')).toBeInTheDocument();
	});

	it('boxes the summary as key and value', () => {
		render(ConfigCard, { title: 'x', summary: [{ k: 'url', v: 'https://git.test' }] });
		expect(screen.getByRole('term')).toHaveTextContent('url');
		expect(screen.getByRole('definition')).toHaveTextContent('https://git.test');
		expect(screen.getByRole('definition')).toHaveAttribute('title', 'https://git.test');
	});

	it('draws no summary box when there is nothing to summarise', () => {
		render(ConfigCard, { title: 'x' });
		expect(screen.queryByRole('term')).not.toBeInTheDocument();
	});

	it('shows a dash for an empty value', () => {
		render(ConfigCard, { title: 'x', summary: [{ k: 'url', v: '' }] });
		expect(screen.getByRole('definition')).toHaveTextContent('—');
	});

	it('marks a secret and keeps it out of the tooltip', () => {
		render(ConfigCard, { title: 'x', summary: [{ k: 'token', v: '•••• set', secret: true }] });
		expect(value('•••• set')).toHaveClass('secret');
		expect(value('•••• set')).toHaveAttribute('title', 'write-only secret');
	});

	it('lets a row say its own tooltip', () => {
		render(ConfigCard, { title: 'x', summary: [{ k: 'url', v: 'git.test', title: 'Base URL' }] });
		expect(value('git.test')).toHaveAttribute('title', 'Base URL');
	});

	it('shows an inline error', () => {
		render(ConfigCard, { title: 'x', error: '401 Unauthorized' });
		expect(screen.getByText('401 Unauthorized')).toBeInTheDocument();
	});

	it('expands the editor only while open', async () => {
		const { rerender } = render(ConfigCard, {
			title: 'x',
			open: false,
			children: text('The editor')
		});
		expect(screen.queryByText('The editor')).not.toBeInTheDocument();

		await rerender({ open: true });
		expect(screen.getByText('The editor')).toBeInTheDocument();

		await rerender({ open: false });
		await waitFor(() => expect(screen.queryByText('The editor')).not.toBeInTheDocument());
	});

	it('opens to nothing when it has no editor', () => {
		const { container } = render(ConfigCard, { title: 'x', open: true });
		expect(container.querySelector('.edit')).not.toBeInTheDocument();
	});
});
