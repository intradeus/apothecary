import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { createRawSnippet, tick } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import Sheet from './Sheet.svelte';

const nav = createRawSnippet(() => ({
	render: () => '<div><button>Runs</button><button>Tickets</button></div>'
}));
const props = { open: true, label: 'Navigation', children: nav };
const sheet = () => screen.getByRole('dialog', { name: 'Navigation' });
const scrim = () => document.querySelector('.sh-scrim')!;
const closed = () => waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

function drag(node: Element, from: { x: number; y: number }, to: { x: number; y: number }) {
	const at = (p: { x: number; y: number }) => ({
		pointerId: 1,
		button: 0,
		clientX: p.x,
		clientY: p.y
	});
	fireEvent.pointerDown(node, at(from));
	fireEvent.pointerMove(node, at(to));
	fireEvent.pointerUp(node, at(to));
}

describe('Sheet', () => {
	it('shows nothing while closed', () => {
		render(Sheet, { ...props, open: false });
		expect(screen.queryByRole('dialog')).toBeNull();
		expect(document.querySelector('.scrim')).toBeNull();
	});

	it('is a modal dialog named by its label, under body', () => {
		const { container } = render(Sheet, props);
		expect(sheet()).toHaveAttribute('aria-modal', 'true');
		expect(sheet().parentElement).toBe(document.body);
		expect(container).not.toContainElement(sheet());
		expect(scrim().parentElement).toBe(document.body);
	});

	it.each(['left', 'right', 'bottom'] as const)('docks to the %s edge', (side) => {
		render(Sheet, { ...props, side });
		expect(sheet()).toHaveClass(side);
	});

	it('docks left by default', () => {
		render(Sheet, props);
		expect(sheet()).toHaveClass('left');
	});

	it('closes on a press on the scrim', async () => {
		render(Sheet, props);
		await fireEvent.pointerDown(scrim());
		await closed();
		expect(document.querySelector('.sh-scrim')).toBeNull();
	});

	it('stays open on a press inside', async () => {
		render(Sheet, props);
		await userEvent.click(screen.getByRole('button', { name: 'Runs' }));
		expect(sheet()).toBeInTheDocument();
	});

	it('closes on Esc', async () => {
		render(Sheet, props);
		await userEvent.keyboard('{Escape}');
		await closed();
	});

	it('takes the focus in, and gives it back on the way out', async () => {
		const opener = document.body.appendChild(document.createElement('button'));
		opener.focus();
		const { rerender } = render(Sheet, { ...props, open: false });

		await rerender({ open: true });
		await tick();
		expect(screen.getByRole('button', { name: 'Tickets' })).toHaveFocus();

		await rerender({ open: false });
		await closed();
		expect(opener).toHaveFocus();
		opener.remove();
	});

	it('keeps Tab inside', async () => {
		render(Sheet, props);
		await tick();

		screen.getByRole('button', { name: 'Tickets' }).focus();
		await userEvent.tab();

		expect(screen.getByRole('button', { name: 'Runs' })).toHaveFocus();
	});

	it.each([
		['left', { x: 300, y: 10 }, { x: 50, y: 10 }],
		['right', { x: 50, y: 10 }, { x: 300, y: 10 }],
		['bottom', { x: 10, y: 50 }, { x: 10, y: 300 }]
	] as const)('flicks away toward the %s edge', async (side, from, to) => {
		vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(300);
		vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(300);
		render(Sheet, { ...props, side });

		drag(sheet(), from, to);

		await closed();
	});

	it('springs home from a short drag', async () => {
		vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(300);
		render(Sheet, props);

		drag(sheet(), { x: 300, y: 10 }, { x: 280, y: 10 });

		expect(sheet().style.transform).toBe('translateX(0px)');
	});
});
