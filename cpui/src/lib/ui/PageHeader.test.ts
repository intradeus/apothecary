import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { text } from '$test/helpers';
import PageHeader from './PageHeader.svelte';

const h1 = () => screen.getByRole('heading', { level: 1 });

describe('PageHeader', () => {
	it('puts the accent after the title', () => {
		render(PageHeader, { title: 'Tickets for', accent: 'the bench' });
		expect(h1()).toHaveTextContent(/^Tickets for the bench$/);
		expect(screen.getByText('the bench')).toHaveClass('accent');
	});

	it('puts the accent first when asked', () => {
		render(PageHeader, { title: 'on the bench', accent: 'Tickets', accentFirst: true });
		expect(h1()).toHaveTextContent(/^Tickets on the bench$/);
	});

	it('can be all accent', () => {
		render(PageHeader, { accent: 'The desk' });
		expect(h1()).toHaveTextContent(/^The desk$/);
	});

	it('has no heading row when nothing fills it', () => {
		render(PageHeader, { meta: text('3 runs') });
		expect(screen.queryByRole('heading')).not.toBeInTheDocument();
	});

	it('shows the status chip beside the title', () => {
		render(PageHeader, { title: 'Run', chip: text('running') });
		expect(screen.getByText('running')).toBeInTheDocument();
		expect(h1()).not.toHaveTextContent('running');
	});

	it('shows breadcrumbs with the last one as the current page', () => {
		render(PageHeader, { crumbs: [{ label: 'Desk', href: '/' }, { label: 'Runs' }] });
		expect(screen.getByRole('link', { name: 'Desk' })).toBeInTheDocument();
		expect(screen.getByText('Runs')).toHaveAttribute('aria-current', 'page');
	});

	it('has no breadcrumb row without crumbs', () => {
		render(PageHeader, { title: 'Runs' });
		expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
	});

	it('keeps the breadcrumb row for its right end alone', () => {
		render(PageHeader, { crumbsEnd: text('Layouts') });
		expect(screen.getByText('Layouts')).toBeInTheDocument();
	});

	it('renders every slot it is given', () => {
		render(PageHeader, {
			title: 'Run',
			lead: text('the vial'),
			meta: text('12 tokens'),
			actions: text('Stop'),
			children: text('A strip below')
		});
		const header = screen.getByRole('banner');
		for (const s of ['the vial', '12 tokens', 'Stop', 'A strip below'])
			expect(header).toContainElement(screen.getByText(s));
	});
});
