import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Breadcrumbs, { type Crumb } from './Breadcrumbs.svelte';

const crumbs: Crumb[] = [
	{ label: 'Desk', href: '/' },
	{ label: 'Workshop' },
	{ label: 'PROJ-12', href: '/tickets/12', mono: true },
	{ label: '01ABCD', href: '/runs/1', mono: true }
];

describe('Breadcrumbs', () => {
	it('is a labelled navigation landmark', () => {
		render(Breadcrumbs, { crumbs });
		expect(screen.getByRole('navigation', { name: 'Breadcrumb' })).toBeInTheDocument();
	});

	it('links the crumbs that have an href', () => {
		render(Breadcrumbs, { crumbs });
		expect(screen.getByRole('link', { name: 'Desk' })).toHaveAttribute('href', '/');
		expect(screen.getByRole('link', { name: 'PROJ-12' })).toHaveAttribute('href', '/tickets/12');
	});

	it('leaves a crumb without an href as plain text', () => {
		render(Breadcrumbs, { crumbs });
		expect(screen.getByText('Workshop')).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Workshop' })).not.toBeInTheDocument();
	});

	it('marks the last crumb as the current page and never links it', () => {
		render(Breadcrumbs, { crumbs });
		expect(screen.getByText('01ABCD')).toHaveAttribute('aria-current', 'page');
		expect(screen.queryByRole('link', { name: '01ABCD' })).not.toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Desk' })).not.toHaveAttribute('aria-current');
	});

	it('separates crumbs, with none before the first or after the last', () => {
		render(Breadcrumbs, { crumbs });
		expect(screen.getByRole('navigation').textContent!.replace(/\s/g, '')).toBe(
			'Desk/Workshop/PROJ-12/01ABCD'
		);
	});

	it('sets ids and keys in mono, wherever they fall', () => {
		render(Breadcrumbs, { crumbs });
		expect(screen.getByRole('link', { name: 'PROJ-12' })).toHaveClass('mono');
		expect(screen.getByText('01ABCD')).toHaveClass('mono');
		expect(screen.getByRole('link', { name: 'Desk' })).not.toHaveClass('mono');
	});
});
