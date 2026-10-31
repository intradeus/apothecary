import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import Tabs from './Tabs.svelte';

const items = [
	{ value: 'overview', label: 'Overview' },
	{ value: 'runs', label: 'Runs', count: 0 },
	{ value: 'flows', label: 'Flows', title: 'Flow bindings' }
];
const links = items.map((i) => ({ ...i, href: `/w/${i.value}` }));
const tab = (name: string | RegExp) => screen.getByRole('tab', { name });

describe('Tabs as buttons', () => {
	it('is a named tablist with the value selected', () => {
		render(Tabs, { items, value: 'runs', label: 'Sections' });
		expect(screen.getByRole('tablist', { name: 'Sections' })).toBeInTheDocument();
		expect(tab(/Runs/)).toHaveAttribute('aria-selected', 'true');
		expect(tab('Overview')).toHaveAttribute('aria-selected', 'false');
	});

	it('is one stop on Tab: only the selected one takes it', () => {
		render(Tabs, { items, value: 'runs', label: 'Sections' });
		expect(tab(/Runs/)).toHaveAttribute('tabindex', '0');
		expect(tab('Overview')).toHaveAttribute('tabindex', '-1');
	});

	it('selects on a click', async () => {
		const onchange = vi.fn();
		render(Tabs, { items, value: 'overview', label: 'Sections', onchange });

		await userEvent.click(tab('Flows'));

		expect(onchange).toHaveBeenCalledExactlyOnceWith('flows');
		expect(tab('Flows')).toHaveAttribute('aria-selected', 'true');
		expect(tab('Overview')).toHaveAttribute('aria-selected', 'false');
	});

	it('says nothing when the selected tab is clicked again', async () => {
		const onchange = vi.fn();
		render(Tabs, { items, value: 'overview', label: 'Sections', onchange });

		await userEvent.click(tab('Overview'));

		expect(onchange).not.toHaveBeenCalled();
	});

	it.each([
		['{ArrowRight}', 'overview', 'runs'],
		['{ArrowLeft}', 'runs', 'overview'],
		['{ArrowRight}', 'flows', 'overview'],
		['{ArrowLeft}', 'overview', 'flows'],
		['{Home}', 'flows', 'overview'],
		['{End}', 'overview', 'flows']
	])('%s from %s lands on %s and takes the focus', async (key, from, to) => {
		const onchange = vi.fn();
		render(Tabs, { items, value: from, label: 'Sections', onchange });

		tab(new RegExp(from, 'i')).focus();
		await userEvent.keyboard(key);

		expect(onchange).toHaveBeenCalledExactlyOnceWith(to);
		expect(tab(new RegExp(to, 'i'))).toHaveFocus();
	});

	it('leaves up and down to the page', async () => {
		const onchange = vi.fn();
		render(Tabs, { items, value: 'overview', label: 'Sections', onchange });

		tab('Overview').focus();
		await userEvent.keyboard('{ArrowDown}{ArrowUp}');

		expect(onchange).not.toHaveBeenCalled();
		expect(tab('Overview')).toHaveFocus();
	});

	it('shows a count of zero and the hover title', () => {
		render(Tabs, { items, value: 'overview', label: 'Sections' });
		expect(tab(/Runs/)).toHaveTextContent('Runs0');
		expect(tab('Flows')).toHaveAttribute('title', 'Flow bindings');
	});

	it('marks a tab with its dot', () => {
		render(Tabs, {
			items: [{ value: 'a', label: 'Agent', dot: 'running' as const }],
			value: 'a',
			label: 'Sections'
		});
		expect(tab('Agent').querySelector('.hue-running')).not.toBeNull();
	});
});

describe('Tabs as links', () => {
	it('is a named nav of links, not a tablist', () => {
		render(Tabs, { items: links, value: 'runs', label: 'Sections' });
		expect(screen.getByRole('navigation', { name: 'Sections' })).toBeInTheDocument();
		expect(screen.queryByRole('tablist')).toBeNull();
		expect(screen.getByRole('link', { name: 'Flows' })).toHaveAttribute('href', '/w/flows');
	});

	it('marks the current page only', () => {
		render(Tabs, { items: links, value: 'runs', label: 'Sections' });
		expect(screen.getByRole('link', { name: /Runs/ })).toHaveAttribute('aria-current', 'page');
		expect(screen.getByRole('link', { name: 'Overview' })).not.toHaveAttribute('aria-current');
	});

	it('keeps the scroll position by default', () => {
		render(Tabs, { items: links, value: 'runs', label: 'Sections' });
		expect(screen.getByRole('link', { name: 'Overview' })).toHaveAttribute(
			'data-sveltekit-noscroll'
		);
	});

	it('lets a tab scroll when noscroll is off', () => {
		render(Tabs, { items: links, value: 'runs', label: 'Sections', noscroll: false });
		expect(screen.getByRole('link', { name: 'Overview' })).not.toHaveAttribute(
			'data-sveltekit-noscroll'
		);
	});

	it('turns into links as soon as any item has an href', () => {
		render(Tabs, {
			items: [items[0], { ...items[1], href: '/runs' }],
			value: 'overview',
			label: 'Sections'
		});
		expect(screen.getByRole('navigation', { name: 'Sections' })).toBeInTheDocument();
	});
});
