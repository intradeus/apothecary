import { fireEvent, render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { tab } from '$test/fixtures/desk';
import type { ViewMeta } from './Desk.svelte';
import LeafWindow from './LeafWindow.svelte';
import { leaf, type ViewId } from './tree';

function setup(
	tabs: ViewId[] = ['session', 'logs'],
	over: Partial<{
		act: ViewId;
		w: number;
		focused: boolean;
		maxed: boolean;
		dragging: ViewId | null;
		motion: '' | 'spring' | 'bounce';
		compact: boolean;
		meta: (v: ViewId) => ViewMeta;
	}> = {}
) {
	const { act, ...rest } = over;
	const props = {
		leaf: leaf(tabs, act),
		x: 10.4,
		y: 20.6,
		w: 600,
		h: 400,
		focused: false,
		maxed: false,
		dragging: null,
		motion: '' as const,
		appear: false,
		meta: () => ({}),
		ontabdown: vi.fn(),
		onactivate: vi.fn(),
		onfocus: vi.fn(),
		onsplit: vi.fn(),
		onmax: vi.fn(),
		onclose: vi.fn(),
		onclosetab: vi.fn(),
		...rest
	};
	const { container } = render(LeafWindow, props);
	return { ...props, win: container.querySelector('section')! };
}

describe('LeafWindow', () => {
	it('draws each view as a tab, with the active one selected', () => {
		setup(['session', 'logs'], { act: 'logs' });
		expect(tab('Session')).toHaveAttribute('aria-selected', 'false');
		expect(tab('Logs')).toHaveAttribute('aria-selected', 'true');
		expect(tab('Logs')).toHaveAttribute('title', 'Drag to move or split');
	});

	it('sits at its rect, in whole pixels', () => {
		const { win } = setup();
		expect(win).toHaveStyle({ left: '10px', top: '21px', width: '600px', height: '400px' });
	});

	it.each<[string, ViewMeta, string | null, string | null]>([
		['a number', { count: 3 }, '3', null],
		['a string', { count: 'live' }, 'live', null],
		['a count wanting attention', { count: 2, alert: 'attention' }, '2', 'hot'],
		['a blocked count', { count: 1, alert: 'danger' }, '1', 'bad'],
		['no count', {}, null, null],
		['a zero', { count: 0 }, null, null],
		['an empty string', { count: '' }, null, null]
	])('shows %s on the tab', (_, meta, shown, tone) => {
		setup(['session'], { meta: () => meta });
		const badge = tab('Session').querySelector('.count');
		if (shown === null) return expect(badge).toBeNull();
		expect(badge).toHaveTextContent(shown);
		if (tone) expect(badge).toHaveClass(tone);
		else expect(badge).not.toHaveClass('hot', 'bad');
	});

	it("takes each tab's count from its own view", () => {
		setup(['session', 'logs'], { meta: (v) => (v === 'logs' ? { count: 7 } : {}) });
		expect(tab('Logs')).toHaveTextContent('7');
		expect(tab('Session')).not.toHaveTextContent('7');
	});

	it('dims the tab being dragged', () => {
		setup(['session', 'logs'], { dragging: 'logs' });
		expect(tab('Logs')).toHaveClass('ghosted');
		expect(tab('Session')).not.toHaveClass('ghosted');
	});

	it.each([
		['spring', 'spring'],
		['bounce', 'bounce']
	] as const)('moves on the %s when the desk says so', (motion, cls) => {
		expect(setup(['session'], { motion }).win).toHaveClass(cls);
	});

	it('tracks 1:1 with no motion', () => {
		const { win } = setup();
		expect(win).not.toHaveClass('spring', 'bounce');
	});

	describe('a pointer on a tab', () => {
		it('hands the press to the desk, which decides between a click and a drag', () => {
			const { ontabdown, onactivate } = setup();
			fireEvent.pointerDown(tab('Logs'));
			expect(ontabdown).toHaveBeenCalledExactlyOnceWith('logs', expect.any(Event));
			expect(onactivate).not.toHaveBeenCalled();
		});

		it('does not activate on the click that follows', async () => {
			const { onactivate } = setup();
			await userEvent.click(tab('Logs'));
			expect(onactivate).not.toHaveBeenCalled();
		});
	});

	describe('the keyboard on a tab', () => {
		it.each([
			['Enter', '{Enter}'],
			['Space', ' ']
		])('activates on %s', async (_, key) => {
			const { onactivate } = setup();
			tab('Logs').focus();
			await userEvent.keyboard(key);
			expect(onactivate).toHaveBeenCalledExactlyOnceWith('logs');
		});

		it('ignores any other key', async () => {
			const { onactivate } = setup();
			tab('Logs').focus();
			await userEvent.keyboard('a');
			expect(onactivate).not.toHaveBeenCalled();
		});
	});

	describe("a tab's ×", () => {
		const x = (name: string) => within(tab(name)).getByRole('button', { name: `Hide ${name}` });

		it('hides that view and nothing else', async () => {
			const { onclosetab, ontabdown, onactivate } = setup();
			await userEvent.click(x('Logs'));
			expect(onclosetab).toHaveBeenCalledExactlyOnceWith('logs');
			expect(ontabdown).not.toHaveBeenCalled();
			expect(onactivate).not.toHaveBeenCalled();
		});

		it('keeps its keys from the tab around it', () => {
			const { onactivate } = setup();
			fireEvent.keyDown(x('Logs'), { key: 'Enter' });
			expect(onactivate).not.toHaveBeenCalled();
		});
	});

	describe('splitting', () => {
		it.each([
			['Split right', 'right'],
			['Split down', 'bottom']
		])('%s moves the active tab into a new half', async (name, zone) => {
			const { onsplit } = setup();
			const button = screen.getByRole('button', { name });
			expect(button).not.toHaveAttribute('aria-disabled');
			expect(button).toHaveAttribute('title', name);

			await userEvent.click(button);

			expect(onsplit).toHaveBeenCalledExactlyOnceWith(zone);
		});

		it.each([
			['Split right', 'Split right (needs a second tab)'],
			['Split down', 'Split down (needs a second tab)']
		])('%s says it needs a second tab, and does nothing', async (name, title) => {
			const { onsplit } = setup(['session']);
			const button = screen.getByRole('button', { name });
			expect(button).toHaveAttribute('aria-disabled', 'true');
			expect(button).toHaveAttribute('title', title);

			await userEvent.click(button);

			expect(onsplit).not.toHaveBeenCalled();
		});
	});

	describe('fullscreen', () => {
		it('offers Maximise, with no key cap, on a window in the layout', async () => {
			const { onmax } = setup();
			expect(screen.queryByText('esc')).not.toBeInTheDocument();
			await userEvent.click(screen.getByRole('button', { name: 'Maximise' }));
			expect(onmax).toHaveBeenCalledOnce();
		});

		it('offers Restore, with the esc key cap, on a fullscreen window', async () => {
			const { onmax } = setup(['session'], { maxed: true });
			expect(screen.getByText('esc')).toHaveAttribute('title', 'Restore');
			expect(screen.queryByRole('button', { name: 'Maximise' })).not.toBeInTheDocument();
			await userEvent.click(screen.getByRole('button', { name: 'Restore' }));
			expect(onmax).toHaveBeenCalledOnce();
		});
	});

	it('hides itself from its own button', async () => {
		const { onclose } = setup();
		await userEvent.click(screen.getByRole('button', { name: 'Hide this window' }));
		expect(onclose).toHaveBeenCalledOnce();
	});

	it('takes the focus from a press on its strip, but not on its buttons', () => {
		const { onfocus, win } = setup();
		for (const name of ['Split right', 'Split down', 'Maximise', 'Hide this window'])
			fireEvent.pointerDown(screen.getByRole('button', { name }));
		expect(onfocus).not.toHaveBeenCalled();

		fireEvent.pointerDown(win);
		expect(onfocus).toHaveBeenCalledOnce();
	});

	describe('the hint', () => {
		const meta = () => ({ hint: 'three failures' });

		it("shows the active view's hint when the window has room", () => {
			setup(['session'], { w: 421, meta: (v) => (v === 'session' ? meta() : {}) });
			expect(screen.getByText('three failures')).toBeInTheDocument();
		});

		it('gives it up when the window is narrow', () => {
			setup(['session'], { w: 420, meta });
			expect(screen.queryByText('three failures')).not.toBeInTheDocument();
		});

		it('shows nothing for a view with no hint', () => {
			setup(['session', 'logs'], { act: 'logs', meta: (v) => (v === 'session' ? meta() : {}) });
			expect(screen.queryByText('three failures')).not.toBeInTheDocument();
		});
	});

	describe('compact', () => {
		const compact = (over = {}) => setup(['session', 'logs'], { compact: true, ...over });

		it('activates on a tap, and starts no drag', async () => {
			const { onactivate, ontabdown } = compact();
			await userEvent.click(tab('Logs'));
			expect(onactivate).toHaveBeenCalledExactlyOnceWith('logs');
			expect(ontabdown).not.toHaveBeenCalled();
		});

		it('labels a tab by its view, not by the drag it cannot start', () => {
			compact();
			expect(tab('Logs')).toHaveAttribute('title', 'Logs');
		});

		it('offers nothing to arrange', () => {
			compact();
			expect(screen.queryByRole('button')).not.toBeInTheDocument();
		});

		it('drops the hint', () => {
			compact({ meta: () => ({ hint: 'three failures' }) });
			expect(screen.queryByText('three failures')).not.toBeInTheDocument();
		});
	});
});
