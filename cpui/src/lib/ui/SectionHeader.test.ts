import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import SectionHeader from './SectionHeader.svelte';

describe('SectionHeader', () => {
	it('titles the section with a heading', () => {
		render(SectionHeader, { title: 'Workshops' });
		expect(screen.getByRole('heading', { level: 2, name: 'Workshops' })).toBeInTheDocument();
	});

	it('shows a count of zero, not only positive ones', () => {
		render(SectionHeader, { title: 'Workshops', count: 0 });
		expect(screen.getByText('0')).toHaveClass('count');
	});

	it('has no count unless given one', () => {
		const { container } = render(SectionHeader, { title: 'Workshops' });
		expect(container.querySelector('.count')).toBeNull();
	});

	it('marks a count that needs a human', () => {
		render(SectionHeader, { title: 'Needs you', count: 3, hot: true });
		expect(screen.getByText('3')).toHaveClass('hot');
	});

	it('shows the subtitle and the actions', () => {
		render(SectionHeader, { title: 'Runs', sub: 'last 24h', actions: text('Export') });
		expect(screen.getByText('last 24h')).toBeInTheDocument();
		expect(screen.getByText('Export')).toBeInTheDocument();
	});

	it('links on with "Open" unless told otherwise', () => {
		render(SectionHeader, { title: 'Runs', href: '/runs' });
		expect(screen.getByRole('link', { name: /^Open/ })).toHaveAttribute('href', '/runs');
	});

	it('uses the given link label', () => {
		render(SectionHeader, { title: 'Runs', href: '/runs', linkLabel: 'All runs' });
		expect(screen.getByRole('link', { name: /^All runs/ })).toBeInTheDocument();
	});

	it('has no link without an href', () => {
		render(SectionHeader, { title: 'Runs', linkLabel: 'All runs' });
		expect(screen.queryByRole('link')).toBeNull();
	});

	it('draws the hairline by default and drops it on request', async () => {
		const { container, rerender } = render(SectionHeader, { title: 'Runs' });
		expect(container.querySelector('.grow')).toHaveClass('rule');
		await rerender({ rule: false });
		expect(container.querySelector('.grow')).not.toHaveClass('rule');
	});
});
