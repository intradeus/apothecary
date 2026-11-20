import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import { DESK_KEY, aDesk } from '$test/fixtures/desk';
import { DeskState } from './deskState.svelte';
import LayoutsPopover from './LayoutsPopover.svelte';
import { PRESET_IDS, VIEWS, VIEW_LABEL, leaf, leavesOf } from './tree';

function setup(desk = new DeskState()) {
	desk.menuOpen = true;
	render(LayoutsPopover, { props: { desk, anchor: document.body } });
	return desk;
}

const preset = (name: string) => screen.getByRole('button', { name });
/** A row of the checklist; its status mark is part of its name. */
const win = (name: string) =>
	screen.getByRole('menuitemcheckbox', { name: new RegExp(`${name}$`) });

describe('LayoutsPopover', () => {
	beforeEach(() => localStorage.clear());

	it('stays away while the menu is closed', () => {
		const desk = new DeskState();
		render(LayoutsPopover, { props: { desk, anchor: document.body } });
		expect(screen.queryByText('Layouts')).not.toBeInTheDocument();
	});

	it('says where layouts are kept and how to arrange by hand', () => {
		setup();
		expect(screen.getByText('Layouts')).toBeInTheDocument();
		expect(screen.getByText('saved in this browser')).toBeInTheDocument();
		expect(screen.getByText(/onto another window to stack it/)).toBeInTheDocument();
	});

	describe('the presets', () => {
		it('offers every preset, with the current one pressed', () => {
			setup();
			for (const name of ['Focus', 'Dock', 'Recipe', 'Bench'])
				expect(preset(name)).toHaveAttribute('aria-pressed', 'false');
			expect(preset('Split')).toHaveAttribute('aria-pressed', 'true');
		});

		it.each(PRESET_IDS.filter((id) => id !== 'split'))('applies %s', async (id) => {
			const desk = setup();
			const label = { focus: 'Focus', dock: 'Dock', recipe: 'Recipe', bench: 'Bench' }[id]!;

			await userEvent.click(preset(label));

			expect(desk.preset).toBe(id);
			expect(preset(label)).toHaveAttribute('aria-pressed', 'true');
			expect(preset('Split')).toHaveAttribute('aria-pressed', 'false');
			expect(JSON.parse(localStorage.getItem(DESK_KEY)!).preset).toBe(id);
		});

		it('brings a hidden view back with the preset', async () => {
			const desk = aDesk(leaf(['session']));
			setup(desk);

			await userEvent.click(preset('Focus'));

			expect(desk.visible.size).toBe(VIEWS.length);
		});

		it('replaces the tree each time', async () => {
			const desk = setup();
			await userEvent.click(preset('Focus'));
			expect(leavesOf(desk.tree)).toHaveLength(1);
			await userEvent.click(preset('Recipe'));
			expect(leavesOf(desk.tree)).toHaveLength(4);
		});

		it('keeps Custom out of reach until the person made one', () => {
			setup();
			const custom = preset('Custom');
			expect(custom).toBeDisabled();
			expect(custom).toHaveAttribute('aria-pressed', 'false');
			expect(custom).toHaveAttribute(
				'title',
				'Your own arrangement — drag tabs and gaps to make one'
			);
		});

		it("shows Custom pressed once the arrangement is the person's", async () => {
			const desk = setup();
			await userEvent.click(win(VIEW_LABEL.vitals));

			expect(desk.preset).toBe('custom');
			expect(preset('Custom')).toHaveAttribute('aria-pressed', 'true');
			expect(preset('Split')).toHaveAttribute('aria-pressed', 'false');
		});
	});

	describe('the windows', () => {
		it('lists every view, ticked when it is shown', () => {
			setup(aDesk(leaf(['session', 'logs'])));

			expect(screen.getAllByRole('menuitemcheckbox')).toHaveLength(VIEWS.length);
			expect(win('Session')).toBeChecked();
			expect(win('Logs')).toBeChecked();
			expect(win('Flow')).not.toBeChecked();
		});

		it('counts what is shown', async () => {
			setup(aDesk(leaf(['session', 'logs'])));
			expect(screen.getByText('2 of 15 shown')).toBeInTheDocument();

			await userEvent.click(win('Flow'));

			expect(screen.getByText('3 of 15 shown')).toBeInTheDocument();
		});

		it('hides a shown view', async () => {
			const desk = setup(aDesk(leaf(['session', 'logs'])));

			await userEvent.click(win('Logs'));

			expect(desk.leafOf('logs')).toBeUndefined();
			await waitFor(() => expect(win('Logs')).not.toBeChecked());
		});

		it('brings a hidden view back into a window, in front', async () => {
			const desk = setup(aDesk(leaf(['session'])));

			await userEvent.click(win('Egress'));

			expect(win('Egress')).toBeChecked();
			expect(desk.leafOf('egress')?.act).toBe('egress');
		});

		it('hides the last view too, for the empty bench', async () => {
			const desk = setup(aDesk(leaf(['session'])));

			await userEvent.click(win('Session'));

			expect(desk.tree).toBeNull();
			expect(screen.getByText('0 of 15 shown')).toBeInTheDocument();
		});

		it('fills an empty bench', async () => {
			const desk = setup(aDesk(null));

			await userEvent.click(win('Session'));

			expect(leavesOf(desk.tree).map((l) => l.tabs)).toEqual([['session']]);
		});
	});
});
