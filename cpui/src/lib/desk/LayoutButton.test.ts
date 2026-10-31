import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { activeDesk } from './active.svelte';
import { DeskState } from './desk.svelte';
import LayoutButton from './LayoutButton.svelte';

const fab = () => screen.getByRole('button', { name: 'Windows & layouts' });
const title = () => screen.queryByText('saved in this browser');

function setup() {
	const desk = new DeskState();
	activeDesk.desk = desk;
	render(LayoutButton);
	return desk;
}

describe('LayoutButton', () => {
	beforeEach(() => localStorage.clear());
	afterEach(() => {
		activeDesk.desk = null;
	});

	it('shows nothing with no desk on screen', () => {
		render(LayoutButton);
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it("shows nothing for a phone's desk, which has one window", () => {
		const desk = new DeskState();
		desk.setCompact(true);
		activeDesk.desk = desk;
		render(LayoutButton);
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('comes and goes with the desk', async () => {
		render(LayoutButton);

		activeDesk.desk = new DeskState();
		await tick();
		expect(fab()).toBeInTheDocument();

		activeDesk.desk = null;
		await tick();
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('goes when the screen narrows', async () => {
		const desk = setup();

		desk.setCompact(true);
		await tick();

		expect(screen.queryByRole('button')).not.toBeInTheDocument();
	});

	it('wears its shortcut', () => {
		setup();
		expect(fab()).toHaveTextContent('L');
	});

	it('opens the popover, and says so', async () => {
		const desk = setup();
		expect(fab()).toHaveAttribute('aria-expanded', 'false');

		await userEvent.click(fab());

		expect(desk.menuOpen).toBe(true);
		expect(fab()).toHaveAttribute('aria-expanded', 'true');
		expect(title()).toBeInTheDocument();
	});

	it('closes the popover from the same button, rather than closing and reopening', async () => {
		const desk = setup();
		await userEvent.click(fab());

		await userEvent.click(fab());

		expect(desk.menuOpen).toBe(false);
		await waitFor(() => expect(title()).not.toBeInTheDocument());
	});

	it('opens from the desk too (the L key, the empty bench)', async () => {
		const desk = setup();

		desk.menuOpen = true;

		await waitFor(() => expect(title()).toBeInTheDocument());
		expect(fab()).toHaveAttribute('aria-expanded', 'true');
	});

	it('closes on Esc and gives the focus back to the button', async () => {
		const desk = setup();
		await userEvent.click(fab());

		await userEvent.keyboard('{Escape}');

		expect(desk.menuOpen).toBe(false);
		expect(fab()).toHaveFocus();
	});

	it('closes on a press elsewhere', async () => {
		const desk = setup();
		await userEvent.click(fab());

		await fireEvent.pointerDown(document.body);

		expect(desk.menuOpen).toBe(false);
	});

	it('stays open on a press inside the popover', async () => {
		const desk = setup();
		await userEvent.click(fab());

		await userEvent.click(screen.getByRole('button', { name: 'Dock' }));

		expect(desk.menuOpen).toBe(true);
		expect(desk.preset).toBe('dock');
	});
});
