import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { rect } from '$test/helpers';
import {
	DESK_KEY,
	MIN,
	aDesk,
	aProbe,
	sizeDesk,
	tab,
	twoWindows,
	windowOf,
	windows
} from '$test/fixtures/desk';
import { activeDesk } from './active.svelte';
import { DeskState } from './deskState.svelte';
import Desk, { type ViewMeta } from './Desk.svelte';
import LayoutButton from './LayoutButton.svelte';
import { leaf, leavesOf, split, type ViewId } from './tree';

async function setup(desk: DeskState, meta: (v: ViewId) => ViewMeta = () => ({})) {
	const probe = aProbe();
	render(Desk, { desk, meta, view: probe.view });
	await vi.waitFor(() => expect(desk.tree === null || desk.rects.length > 0).toBe(true));
	return probe;
}

const view = (v: ViewId) => screen.queryByTestId(`view-${v}`);
/** The element a view is hosted in: what the desk positions. */
const host = (v: ViewId) => view(v)!.parentElement!;
const deskEl = () => document.querySelector<HTMLElement>('.desk')!;
const gutters = () => [...document.querySelectorAll<HTMLElement>('.gutter')];
const ghost = () => document.querySelector<HTMLElement>('.ghost');
const preview = () => document.querySelector<HTMLElement>('.drop');
const saved = () => JSON.parse(localStorage.getItem(DESK_KEY) ?? 'null');
const button = (win: HTMLElement, name: string) => within(win).getByRole('button', { name });

const pointer = (x: number, y: number, pointerId = 1) => ({ clientX: x, clientY: y, pointerId });
const press = (el: Element, x = 0, y = 0, button = 0) =>
	fireEvent.pointerDown(el, { ...pointer(x, y), button });
const drag = (el: Element, x: number, y: number) => fireEvent.pointerMove(el, pointer(x, y));
const release = (el: Element, x = 0, y = 0) => fireEvent.pointerUp(el, pointer(x, y));

/** Press a tab and carry it to a point on the desk, still held. */
async function carry(label: string, x: number, y: number) {
	const t = tab(label);
	await press(t, 100, 15);
	await drag(t, x, y);
	return t;
}

describe('Desk', () => {
	beforeEach(() => {
		localStorage.clear();
		sizeDesk();
	});
	afterEach(() => {
		vi.useRealTimers();
		activeDesk.desk = null;
		document.body.style.cursor = '';
	});

	describe('the windows', () => {
		it('draws one window per leaf, sharing out the space by the tree', async () => {
			await setup(twoWindows().desk);

			expect(windows()).toEqual([['Session', 'Logs', 'Terminal'], ['Flow']]);
			expect(windowOf('Session')).toHaveStyle({ left: '0px', width: '596px', height: '800px' });
			expect(windowOf('Flow')).toHaveStyle({ left: '604px', width: '596px' });
		});

		it("passes each view's count to its tab", async () => {
			await setup(twoWindows().desk, (v) => (v === 'flow' ? { count: 4 } : {}));
			expect(tab('Flow')).toHaveTextContent('4');
		});

		it('draws no windows until it has a size', async () => {
			sizeDesk(0, 0);
			render(Desk, { desk: twoWindows().desk, meta: () => ({}), view: aProbe().view });
			await tick();

			expect(windows()).toEqual([]);
		});
	});

	describe('the views', () => {
		it("mounts only what is on screen: each window's active tab", async () => {
			const probe = await setup(twoWindows().desk);

			expect(view('session')).toBeInTheDocument();
			expect(view('flow')).toBeInTheDocument();
			expect(probe.mounts('logs')).toBe(0);
		});

		it('tells a view where it is drawn: its window, under the tab strip', async () => {
			const probe = await setup(twoWindows().desk);

			expect(probe.place('session')).toEqual({ visible: true, w: 594, h: 761 });
			expect(host('flow')).toHaveStyle({ left: '605px', top: '38px' });
		});

		it('mounts a view the first time its tab is shown', async () => {
			const probe = await setup(twoWindows().desk);

			await userEvent.click(tab('Logs'));

			expect(probe.mounts('logs')).toBe(1);
			expect(probe.place('logs').visible).toBe(true);
		});

		it('keeps a view mounted behind another tab, and does not remount it', async () => {
			const probe = await setup(twoWindows().desk);

			await userEvent.click(tab('Logs'));
			expect(probe.place('session')).toEqual({ visible: false, w: 0, h: 0 });
			expect(host('session')).toHaveClass('tab');

			await userEvent.click(tab('Session'));
			expect(probe.place('session').visible).toBe(true);
			expect(host('session')).not.toHaveClass('tab');
			expect(probe.mounts('session')).toBe(1);
			expect(probe.unmounts('session')).toBe(0);
		});

		it('unmounts a view that is hidden', async () => {
			const probe = await setup(twoWindows().desk);

			await userEvent.click(button(windowOf('Session'), 'Hide Session'));

			await waitFor(() => expect(probe.unmounts('session')).toBe(1));
			expect(tab('Logs')).toHaveAttribute('aria-selected', 'true');
			expect(probe.place('logs').visible).toBe(true);
		});
	});

	describe('the tabs', () => {
		it('selects a clicked tab and saves it, leaving the preset as it was', async () => {
			const desk = new DeskState();
			await setup(desk);

			await userEvent.click(tab('Logs'));

			expect(tab('Logs')).toHaveAttribute('aria-selected', 'true');
			expect(tab('Session')).toHaveAttribute('aria-selected', 'false');
			expect(desk.preset).toBe('split');
			expect(saved().preset).toBe('split');
			expect(leavesOf(saved().tree).find((l) => l.tabs.includes('logs'))?.act).toBe('logs');
		});

		it.each([
			['Enter', '{Enter}'],
			['Space', ' ']
		])('selects a focused tab on %s', async (_, key) => {
			await setup(twoWindows().desk);

			tab('Terminal').focus();
			await userEvent.keyboard(key);

			expect(tab('Terminal')).toHaveAttribute('aria-selected', 'true');
		});

		it('does not select on a right-button press', async () => {
			await setup(twoWindows().desk);

			const t = tab('Logs');
			await press(t, 10, 10, 2);
			await release(t, 10, 10);

			expect(t).toHaveAttribute('aria-selected', 'false');
		});

		it('hands a press on a tab to its window', async () => {
			const { desk, b } = twoWindows();
			await setup(desk);

			await userEvent.click(tab('Flow'));

			expect(desk.focus).toBe(b.id);
			expect(windowOf('Flow')).toHaveClass('focused');
			expect(windowOf('Session')).not.toHaveClass('focused');
		});
	});

	describe('the focus', () => {
		it("follows a press on a window's strip", async () => {
			const { desk, a } = twoWindows();
			await setup(desk);

			await fireEvent.pointerDown(windowOf('Session'));

			expect(desk.focus).toBe(a.id);
		});

		it('follows a press inside a view', async () => {
			const { desk, b } = twoWindows();
			await setup(desk);

			await fireEvent.pointerDown(view('flow')!);

			expect(desk.focus).toBe(b.id);
		});
	});

	describe('splitting', () => {
		it('moves the active tab into a new window to the right', async () => {
			const { desk } = twoWindows();
			const probe = await setup(desk);

			await userEvent.click(button(windowOf('Session'), 'Split right'));

			expect(windows()).toEqual([['Logs', 'Terminal'], ['Session'], ['Flow']]);
			expect(windowOf('Session')).toHaveClass('focused');
			expect(desk.preset).toBe('custom');
			expect(saved().preset).toBe('custom');
			// Moved, not rebuilt: a terminal or a stream survives it.
			expect(probe.mounts('session')).toBe(1);
			expect(probe.unmounts('session')).toBe(0);
		});

		it('moves the active tab into a new window below', async () => {
			await setup(twoWindows().desk);

			await userEvent.click(button(windowOf('Session'), 'Split down'));

			expect(windows()).toEqual([['Logs', 'Terminal'], ['Session'], ['Flow']]);
			expect(windowOf('Session')).toHaveStyle({ left: '0px', top: '404px', height: '396px' });
			expect(windowOf('Logs')).toHaveStyle({ top: '0px', height: '396px' });
		});

		it('leaves a one-tab window alone', async () => {
			const { desk } = twoWindows();
			const tree = desk.tree;
			await setup(desk);

			await userEvent.click(button(windowOf('Flow'), 'Split right'));

			expect(desk.tree).toBe(tree);
			expect(windows()).toHaveLength(2);
		});

		it('springs the windows to their new rects, then stops', async () => {
			vi.useFakeTimers();
			const { desk } = twoWindows();
			await setup(desk);

			await fireEvent.click(button(windowOf('Session'), 'Split right'));
			expect(windowOf('Flow')).toHaveClass('spring');
			expect(host('flow')).toHaveClass('spring');

			vi.runAllTimers();
			await tick();

			expect(windowOf('Flow')).not.toHaveClass('spring');
		});
	});

	describe('hiding', () => {
		it('hides one tab from its ×', async () => {
			await setup(twoWindows().desk);

			await userEvent.click(button(windowOf('Session'), 'Hide Logs'));

			expect(windows()).toEqual([['Session', 'Terminal'], ['Flow']]);
		});

		it("gives the space back when a window's last tab goes", async () => {
			await setup(twoWindows().desk);

			await userEvent.click(button(windowOf('Flow'), 'Hide Flow'));

			await waitFor(() => expect(windows()).toEqual([['Session', 'Logs', 'Terminal']]));
			expect(windowOf('Session')).toHaveStyle({ width: '1200px' });
		});

		it('hides a whole window from its own button', async () => {
			const { desk } = twoWindows();
			const probe = await setup(desk);

			await userEvent.click(button(windowOf('Session'), 'Hide this window'));

			await waitFor(() => expect(windows()).toEqual([['Flow']]));
			expect(desk.visible.has('logs')).toBe(false);
			expect(saved().preset).toBe('custom');
			await waitFor(() => expect(probe.unmounts('session')).toBe(1));
		});
	});

	describe('a preset', () => {
		it("lays out its windows and shows each one's first view", async () => {
			const desk = aDesk(leaf(['flow']));
			const probe = await setup(desk);

			desk.applyPreset('dock');

			await waitFor(() => expect(windows()).toHaveLength(5));
			expect(tab('Terminal')).toHaveAttribute('aria-selected', 'true');
			expect(probe.place('terminal').visible).toBe(true);
			expect(probe.mounts('flow')).toBe(1);
		});

		it('restores a fullscreen window: the layout it hid just changed', async () => {
			const { desk, a } = twoWindows();
			await setup(desk);
			desk.toggleMax(a.id);
			await waitFor(() => expect(windows()).toHaveLength(1));

			desk.applyPreset('focus');

			await waitFor(() => expect(deskEl()).not.toHaveClass('full'));
			expect(screen.queryByText('esc')).not.toBeInTheDocument();
		});
	});

	describe('fullscreen', () => {
		async function maximised() {
			const { desk, a, b } = twoWindows();
			const probe = await setup(desk);
			await userEvent.click(button(windowOf('Session'), 'Maximise'));
			await waitFor(() => expect(windows()).toHaveLength(1));
			return { desk, a, b, probe };
		}

		it('takes the whole screen with one window, tabs and all', async () => {
			vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue(
				rect(200, 50, 1200, 800)
			);
			const { probe } = await maximised();

			expect(windows()).toEqual([['Session', 'Logs', 'Terminal']]);
			// From the desk's own corner: past its edges, by the sidebar and top bar.
			expect(windowOf('Session')).toHaveStyle({
				left: '-192px',
				top: '-42px',
				width: '1008px',
				height: '752px'
			});
			expect(probe.place('session')).toEqual({ visible: true, w: 1006, h: 713 });
			expect(deskEl()).toHaveClass('full');
			expect(screen.getByText('esc')).toBeInTheDocument();
			expect(gutters()).toHaveLength(0);
		});

		it("sets the other windows' views aside, still mounted", async () => {
			const { probe } = await maximised();

			expect(probe.place('flow').visible).toBe(false);
			expect(host('flow')).toHaveClass('away');
			expect(probe.unmounts('flow')).toBe(0);
		});

		it('comes back exactly as it was from the Restore button', async () => {
			const { probe } = await maximised();

			await userEvent.click(screen.getByRole('button', { name: 'Restore' }));

			await waitFor(() => expect(windows()).toHaveLength(2));
			expect(windowOf('Session')).toHaveStyle({ left: '0px', width: '596px' });
			expect(probe.place('flow').visible).toBe(true);
			expect(host('flow')).not.toHaveClass('away');
		});

		it('is never saved, and leaves the tree alone', async () => {
			const { desk } = twoWindows();
			await setup(desk);
			const tree = desk.tree;

			await userEvent.click(button(windowOf('Session'), 'Maximise'));

			expect(saved()).toBeNull();
			expect(desk.tree).toBe(tree);
		});

		it('stays fullscreen across a tab switch', async () => {
			const { desk, a } = await maximised();

			await userEvent.click(tab('Logs'));

			expect(desk.max).toBe(a.id);
			expect(tab('Logs')).toHaveAttribute('aria-selected', 'true');
		});

		it('restores when a tab of the window is hidden', async () => {
			const { desk } = await maximised();

			await userEvent.click(button(windowOf('Session'), 'Hide Logs'));

			expect(desk.max).toBeNull();
			await waitFor(() => expect(windows()).toHaveLength(2));
		});

		describe('Esc', () => {
			const esc = async (target: Element = document.body) => {
				await fireEvent.keyDown(target, { key: 'Escape' });
				vi.runAllTimers();
				await tick();
			};

			async function full() {
				vi.useFakeTimers();
				const { desk, a } = twoWindows();
				desk.toggleMax(a.id);
				const probe = await setup(desk);
				return { desk, a, probe };
			}

			it('puts everything back', async () => {
				const { desk } = await full();

				await esc();

				expect(desk.max).toBeNull();
				await vi.waitFor(() => expect(windows()).toHaveLength(2));
			});

			it('ignores any other key', async () => {
				const { desk, a } = await full();

				await fireEvent.keyDown(document.body, { key: 'Enter' });
				vi.runAllTimers();

				expect(desk.max).toBe(a.id);
			});

			it('stays out of a terminal: vim needs it', async () => {
				const { desk, a } = await full();
				const term = document.createElement('div');
				term.className = 'xterm';
				const input = term.appendChild(document.createElement('textarea'));
				view('session')!.append(term);

				await esc(input);

				expect(desk.max).toBe(a.id);
				expect(screen.getByRole('button', { name: 'Restore' })).toBeInTheDocument();
			});

			it('leaves it to a modal that is open', async () => {
				const { desk, a } = await full();
				const scrim = document.body.appendChild(document.createElement('div'));
				scrim.className = 'scrim';

				await esc();
				scrim.remove();

				expect(desk.max).toBe(a.id);
			});

			it('lets an open popover take it first, and restores on the next', async () => {
				const { desk, a } = await full();
				activeDesk.desk = desk;
				render(LayoutButton);
				await tick();
				desk.menuOpen = true;
				await tick();
				await tick();

				await esc();

				expect(desk.menuOpen).toBe(false);
				expect(desk.max).toBe(a.id);

				await esc();

				expect(desk.max).toBeNull();
			});

			it('cancels a drag rather than leaving fullscreen', async () => {
				const { desk, a } = await full();
				await carry('Logs', 300, 400);
				expect(ghost()).toBeInTheDocument();

				await esc();

				expect(desk.max).toBe(a.id);
				expect(desk.drag).toBeNull();
			});
		});
	});

	describe('dragging a tab', () => {
		it('stays a click below the intent distance', async () => {
			await setup(twoWindows().desk);
			const t = tab('Logs');

			await press(t, 100, 15);
			await drag(t, 103, 19); // 5px of travel
			expect(ghost()).not.toBeInTheDocument();
			await release(t, 103, 19);

			expect(t).toHaveAttribute('aria-selected', 'true');
			expect(windows()).toHaveLength(2);
		});

		it('lifts at the intent distance', async () => {
			await setup(twoWindows().desk);
			const t = tab('Logs');

			await press(t, 100, 15);
			await drag(t, 106, 15);

			expect(ghost()).toHaveTextContent('Logs');
			expect(t).toHaveClass('ghosted');
			await release(t, 106, 15);
		});

		it('selects a tab dropped back on its own window', async () => {
			const { desk } = twoWindows();
			await setup(desk);
			const tree = leavesOf(desk.tree).map((l) => l.tabs);

			const t = await carry('Logs', 300, 400);
			await release(t, 300, 400);

			expect(t).toHaveAttribute('aria-selected', 'true');
			expect(leavesOf(desk.tree).map((l) => l.tabs)).toEqual(tree);
		});

		it('keeps the ghost under the pointer where it grabbed the tab', async () => {
			await setup(twoWindows().desk);
			const t = tab('Logs');
			vi.spyOn(t, 'getBoundingClientRect').mockReturnValue(rect(90, 5, 60, 24));

			await press(t, 100, 15); // 10 in, 10 down
			await drag(t, 300, 400);

			expect(ghost()).toHaveStyle({ left: '290px', top: '390px' });
		});

		it('works in desk coordinates, wherever the desk sits', async () => {
			await setup(twoWindows().desk);
			const t = tab('Logs');
			vi.spyOn(deskEl(), 'getBoundingClientRect').mockReturnValue(rect(200, 50, 1200, 800));

			await press(t, 100, 15);
			await drag(t, 1100, 450); // 900, 400 on the desk: the middle of Flow

			expect(preview()).toHaveTextContent('Stack here');
			expect(preview()).toHaveStyle({ left: '604px', width: '596px' });
		});

		it('follows only the pointer that pressed', async () => {
			await setup(twoWindows().desk);
			const t = tab('Logs');
			await press(t, 100, 15);

			await fireEvent.pointerMove(t, pointer(900, 400, 2));
			expect(ghost()).not.toBeInTheDocument();

			await fireEvent.pointerUp(t, pointer(900, 400, 2));
			await drag(t, 900, 400);
			expect(ghost()).toBeInTheDocument();
			await release(t, 900, 400);
		});

		it.each([
			['the middle of a window', 900, 400, 'Stack here', '604px', '596px'],
			['the tab strip, even at its edge', 610, 10, 'Stack here', '604px', '596px'],
			['the left edge', 620, 400, 'Split left', '604px', '298px'],
			['the right edge', 1190, 400, 'Split right', '902px', '298px'],
			['the top edge', 900, 60, 'Split top', '604px', '596px'],
			['the bottom edge', 900, 790, 'Split bottom', '604px', '596px']
		])('previews %s', async (_, x, y, label, left, width) => {
			await setup(twoWindows().desk);
			const t = await carry('Logs', x, y);

			expect(preview()).toHaveTextContent(label);
			expect(preview()).toHaveStyle({ left, width });

			await release(t, x, y);
		});

		it('previews nothing over a gap', async () => {
			await setup(twoWindows().desk);
			const t = await carry('Logs', 600, 400);

			expect(ghost()).toBeInTheDocument();
			expect(preview()).not.toBeInTheDocument();

			await release(t, 600, 400);
		});

		it('takes the preview away as the pointer leaves a window', async () => {
			await setup(twoWindows().desk);
			const t = await carry('Logs', 900, 400);
			expect(preview()).toBeInTheDocument();

			await drag(t, 600, 400);

			await waitFor(() => expect(preview()).not.toBeInTheDocument());
			await release(t, 600, 400);
		});

		it('stacks a tab dropped on a window, in front, keeping the view mounted', async () => {
			const { desk, b } = twoWindows();
			const probe = await setup(desk);
			await userEvent.click(tab('Logs'));

			const t = await carry('Logs', 900, 400);
			await release(t, 900, 400);

			expect(windows()).toEqual([
				['Session', 'Terminal'],
				['Flow', 'Logs']
			]);
			expect(tab('Logs')).toHaveAttribute('aria-selected', 'true');
			expect(desk.focus).toBe(b.id);
			expect(host('logs')).toHaveStyle({ left: '605px' });
			expect(probe.mounts('logs')).toBe(1);
			expect(probe.unmounts('logs')).toBe(0);
			expect(saved().preset).toBe('custom');
			await waitFor(() => {
				expect(ghost()).not.toBeInTheDocument();
				expect(preview()).not.toBeInTheDocument();
			});
		});

		it.each([
			['left', 620, 400, [['Session', 'Terminal'], ['Logs'], ['Flow']]],
			['right', 1190, 400, [['Session', 'Terminal'], ['Flow'], ['Logs']]],
			['top', 900, 60, [['Session', 'Terminal'], ['Logs'], ['Flow']]],
			['bottom', 900, 790, [['Session', 'Terminal'], ['Flow'], ['Logs']]]
		])('splits a window when dropped on its %s edge', async (_, x, y, want) => {
			const { desk } = twoWindows();
			await setup(desk);

			const t = await carry('Logs', x, y);
			await release(t, x, y);

			expect(windows()).toEqual(want);
			expect(windowOf('Logs')).toHaveClass('focused');
			expect(desk.preset).toBe('custom');
		});

		it('leaves a one-tab window alone when it is dropped on itself', async () => {
			const { desk } = twoWindows();
			const tree = leavesOf(desk.tree).map((l) => l.tabs);
			await setup(desk);

			const t = await carry('Flow', 620, 400);
			await release(t, 620, 400);

			expect(leavesOf(desk.tree).map((l) => l.tabs)).toEqual(tree);
			expect(windows()).toHaveLength(2);
		});

		it('takes a window away when its only tab is dragged off', async () => {
			await setup(twoWindows().desk);

			const t = await carry('Flow', 300, 400);
			await release(t, 300, 400);

			await waitFor(() => expect(windows()).toEqual([['Session', 'Logs', 'Terminal', 'Flow']]));
		});

		it('flies home when released over nothing, then lets go', async () => {
			vi.useFakeTimers();
			const { desk } = twoWindows();
			await setup(desk);
			const tree = desk.tree;
			const t = tab('Logs');
			vi.spyOn(t, 'getBoundingClientRect').mockReturnValue(rect(90, 5, 60, 24));

			await press(t, 100, 15);
			await drag(t, 600, 400);
			await release(t, 600, 400);

			expect(ghost()).toHaveClass('back');
			expect(ghost()).toHaveStyle({ left: '90px', top: '5px' });
			expect(desk.tree).toBe(tree);

			vi.runAllTimers();
			await tick();

			expect(desk.drag).toBeNull();
		});

		it.each([
			['Esc', () => fireEvent.keyDown(window, { key: 'Escape' })],
			['a cancelled pointer', () => fireEvent.pointerCancel(tab('Logs'), pointer(900, 400))]
		])('drops nothing on %s, even over a window', async (_, cancel) => {
			vi.useFakeTimers();
			const { desk } = twoWindows();
			await setup(desk);
			const tree = desk.tree;
			await carry('Logs', 900, 400);

			await cancel();

			expect(ghost()).toHaveClass('back');
			expect(desk.drop).toBeNull();
			expect(desk.tree).toBe(tree);
		});

		it('cancels a press that has not moved, and selects nothing', async () => {
			await setup(twoWindows().desk);
			const t = tab('Logs');
			await press(t, 100, 15);

			await fireEvent.keyDown(window, { key: 'Escape' });
			await release(t, 100, 15);

			expect(t).toHaveAttribute('aria-selected', 'false');
		});

		it('stops listening once it is over', async () => {
			await setup(twoWindows().desk);
			const t = tab('Logs');
			await press(t, 100, 15);
			await release(t, 100, 15);

			await drag(t, 900, 400);

			expect(ghost()).not.toBeInTheDocument();
		});

		it('closes the layouts menu', async () => {
			const { desk } = twoWindows();
			await setup(desk);
			desk.menuOpen = true;

			await press(tab('Logs'), 100, 15);

			expect(desk.menuOpen).toBe(false);
		});

		it('shows the grabbing cursor while it is held, and blocks the views', async () => {
			await setup(twoWindows().desk);

			const t = await carry('Logs', 900, 400);
			expect(document.body.style.cursor).toBe('grabbing');
			expect(deskEl()).toHaveClass('busy');

			await release(t, 900, 400);
			expect(document.body.style.cursor).toBe('');
		});
	});

	describe('dragging a gutter', () => {
		it('tracks the pointer 1:1, saving only on release', async () => {
			const { desk } = twoWindows();
			await setup(desk);
			const [g] = gutters();

			await press(g, 600, 400);
			await drag(g, 700, 400);

			expect(windowOf('Session')).toHaveStyle({ width: '696px' });
			expect(windowOf('Flow')).toHaveStyle({ left: '704px', width: '496px' });
			expect(windowOf('Session')).not.toHaveClass('spring');
			expect(saved()).toBeNull();

			await release(g, 700, 400);

			expect(desk.preset).toBe('custom');
			expect(saved().tree.s[0]).toBeCloseTo(696 / 1192);
			expect(windowOf('Session')).not.toHaveClass('bounce');
		});

		it('resizes a column by height', async () => {
			const top = leaf(['flow']);
			const bottom = leaf(['session']);
			await setup(aDesk(split('col', [0.5, 0.5], [top, bottom])));
			const [g] = gutters();

			await press(g, 600, 400);
			await fireEvent.pointerMove(g, pointer(100, 500));

			expect(windowOf('Flow')).toHaveStyle({ width: '1200px', height: '496px' });
			expect(document.body.style.cursor).toBe('row-resize');

			await release(g, 100, 500);
			expect(document.body.style.cursor).toBe('');
		});

		it('resizes only the panes either side of it', async () => {
			const a = leaf(['flow']);
			const b = leaf(['session']);
			const c = leaf(['logs']);
			await setup(aDesk(split('row', [0.5, 0.5], [a, split('col', [0.5, 0.5], [b, c])])));
			const [outer, inner] = gutters();
			expect(outer).toHaveClass('row');

			await press(inner, 900, 400);
			await drag(inner, 900, 500);
			await release(inner, 900, 500);

			expect(windowOf('Session')).toHaveStyle({ height: '496px' });
			expect(windowOf('Logs')).toHaveStyle({ top: '504px', height: '296px' });
			expect(windowOf('Flow')).toHaveStyle({ width: '596px', height: '800px' });
		});

		it('shows which gutter is held', async () => {
			await setup(twoWindows().desk);
			const [g] = gutters();

			await press(g, 600, 400);
			expect(g).toHaveClass('held');
			expect(document.body.style.cursor).toBe('col-resize');

			await release(g, 600, 400);
			expect(g).not.toHaveClass('held');
		});

		it('resists past the smallest pane, without reaching where the pointer is', async () => {
			await setup(twoWindows().desk);
			const [g] = gutters();

			await press(g, 596, 400);
			await drag(g, 46, 400); // asks for 46px: 74px past the limit

			const width = parseFloat(windowOf('Session').style.width);
			expect(width).toBeLessThan(MIN);
			expect(width).toBeGreaterThan(46);

			await release(g, 46, 400);
		});

		it.each([
			['the left', 46, MIN],
			['the right', 1190, 1192 - MIN]
		])('bounces back to the limit when released past %s', async (_, x, limit) => {
			vi.useFakeTimers();
			const { desk } = twoWindows();
			await setup(desk);
			const [g] = gutters();

			await press(g, 596, 400);
			await drag(g, x, 400);
			await release(g, x, 400);

			expect(windowOf('Session')).toHaveStyle({ width: `${limit}px` });
			expect(windowOf('Session')).toHaveClass('bounce');

			vi.runAllTimers();
			await tick();

			expect(windowOf('Session')).not.toHaveClass('bounce');
			expect(desk.motion).toBe('');
		});

		it('does not jump a pane already under the smallest size', async () => {
			const a = leaf(['flow']);
			const b = leaf(['session']);
			await setup(aDesk(split('row', [0.05, 0.95], [a, b])));
			const [g] = gutters();
			const before = windowOf('Flow').style.width;

			await press(g, 60, 400);
			await drag(g, 61, 400);
			await drag(g, 60, 400);
			await release(g, 60, 400);

			expect(windowOf('Flow').style.width).toBe(before);
			expect(windowOf('Flow')).not.toHaveClass('bounce');
		});

		it('ignores a right-button press', async () => {
			await setup(twoWindows().desk);
			const [g] = gutters();

			await press(g, 600, 400, 2);
			await drag(g, 700, 400);

			expect(windowOf('Session')).toHaveStyle({ width: '596px' });
		});

		it('follows only the pointer that pressed', async () => {
			await setup(twoWindows().desk);
			const [g] = gutters();
			await press(g, 600, 400);

			await fireEvent.pointerMove(g, pointer(700, 400, 2));
			await fireEvent.pointerUp(g, pointer(700, 400, 2));

			expect(windowOf('Session')).toHaveStyle({ width: '596px' });
			expect(g).toHaveClass('held');
			await release(g, 600, 400);
		});

		it('commits where it was on a cancelled pointer', async () => {
			await setup(twoWindows().desk);
			const [g] = gutters();
			await press(g, 600, 400);
			await drag(g, 700, 400);

			await fireEvent.pointerCancel(g, pointer(700, 400));

			expect(g).not.toHaveClass('held');
			expect(saved().tree.s[0]).toBeCloseTo(696 / 1192);
		});

		it('stops listening once released', async () => {
			await setup(twoWindows().desk);
			const [g] = gutters();
			await press(g, 600, 400);
			await release(g, 600, 400);

			await drag(g, 700, 400);

			expect(windowOf('Session')).toHaveStyle({ width: '596px' });
		});

		it('leaves the preset alone when a gutter is only clicked', async () => {
			const desk = new DeskState();
			await setup(desk);
			const [g] = gutters();

			await press(g, 600, 120);
			await release(g, 600, 120);

			expect(desk.preset).toBe('split');
		});
	});

	describe('the view in the corner', () => {
		const clear = (v: ViewId) => host(v).style.getPropertyValue('--fab-clear');

		it('makes room for the Layout button there, and only there', async () => {
			await setup(twoWindows().desk);

			expect(clear('flow')).toBe('52px');
			expect(clear('session')).toBe('');
		});

		it('moves the room with the windows', async () => {
			const { desk } = twoWindows();
			await setup(desk);

			desk.closeView('flow');

			await waitFor(() => expect(clear('session')).toBe('52px'));
		});
	});

	describe('the empty bench', () => {
		it('says every window is hidden, with the way back', async () => {
			const desk = aDesk(null);
			await setup(desk);

			expect(screen.getByText('An empty bench')).toBeInTheDocument();
			expect(windows()).toEqual([]);

			const open = screen.getByRole('button', { name: /Open Windows/ });
			expect(open).toHaveAttribute('aria-expanded', 'false');
			await userEvent.click(open);

			expect(desk.menuOpen).toBe(true);
			expect(open).toHaveAttribute('aria-expanded', 'true');
		});

		it('appears when the last window is hidden, and goes with the first one back', async () => {
			const desk = aDesk(leaf(['session']));
			await setup(desk);

			await userEvent.click(screen.getByRole('button', { name: 'Hide Session' }));
			expect(await screen.findByText('An empty bench')).toBeInTheDocument();

			desk.showView('flow');

			await waitFor(() => expect(windows()).toEqual([['Flow']]));
			await waitFor(() => expect(screen.queryByText('An empty bench')).not.toBeInTheDocument());
		});

		it('closes the menu from the same button', async () => {
			const desk = aDesk(null);
			activeDesk.desk = desk;
			render(LayoutButton);
			await setup(desk);
			const open = screen.getByRole('button', { name: /Open Windows/ });

			await userEvent.click(open);
			await userEvent.click(open);

			expect(desk.menuOpen).toBe(false);
		});

		it('opens the menu after a press elsewhere closed it', async () => {
			const desk = aDesk(null);
			activeDesk.desk = desk;
			render(LayoutButton);
			await setup(desk);
			const open = screen.getByRole('button', { name: /Open Windows/ });

			await userEvent.click(open);
			await userEvent.click(document.body);
			expect(desk.menuOpen).toBe(false);

			await userEvent.click(open);
			expect(desk.menuOpen).toBe(true);
		});

		it('toggles the menu from the keyboard', async () => {
			const desk = aDesk(null);
			activeDesk.desk = desk;
			render(LayoutButton);
			await setup(desk);
			screen.getByRole('button', { name: /Open Windows/ }).focus();

			await userEvent.keyboard('{Enter}');
			expect(desk.menuOpen).toBe(true);

			await userEvent.keyboard('{Enter}');
			expect(desk.menuOpen).toBe(false);
		});
	});

	describe('compact', () => {
		it('holds one window of every view, tapped between, with nothing to arrange', async () => {
			const desk = new DeskState();
			desk.setCompact(true);
			const probe = await setup(desk);

			expect(windows()).toHaveLength(1);
			expect(windows()[0]).toHaveLength(15);
			expect(probe.place('session').visible).toBe(true);
			expect(gutters()).toHaveLength(0);
			expect(screen.queryByRole('button')).not.toBeInTheDocument();

			await userEvent.click(tab('Logs'));

			expect(probe.place('logs').visible).toBe(true);
			expect(saved()).toBeNull();
		});

		it('starts no drag', async () => {
			const desk = new DeskState();
			desk.setCompact(true);
			await setup(desk);
			const t = tab('Logs');

			await press(t, 100, 15);
			await drag(t, 400, 400);

			expect(ghost()).not.toBeInTheDocument();
		});
	});

	it('remembers the layout across a mount', async () => {
		const first = new DeskState();
		first.applyPreset('recipe');
		first.closeView('vitals');

		const desk = new DeskState();
		await setup(desk);

		expect(desk.preset).toBe('custom');
		expect(windows()).toHaveLength(3);
		expect(desk.visible.has('vitals')).toBe(false);
	});
});
