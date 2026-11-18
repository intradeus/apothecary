import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import type { FileLoc, RunCheckout } from '$lib/domain';
import FileTabs from './FileTabs.svelte';

type Tab = { key: string; loc: FileLoc; stale: boolean; deleted: boolean };

const HINT = 'Drag or Alt+Shift+←/→ to move; middle-click to close';
const app: RunCheckout = {
	name: 'app',
	dir: 'repo',
	url: 'https://git.test/app',
	branch: 'feat/x',
	source: 'main',
	primary: true
};
const lib: RunCheckout = { ...app, name: 'lib', dir: 'repos/lib', primary: false };

const tab = (key: string, loc: FileLoc, over: Partial<Tab> = {}): Tab => ({
	key,
	loc,
	stale: false,
	deleted: false,
	...over
});
const three = (): Tab[] => [
	tab('a', { repo: '', path: 'src/main.go' }),
	tab('b', { repo: 'lib', path: 'b.ts' }),
	tab('c', { abs: '/etc/hosts' })
];

function mount(props: { tabs?: Tab[]; active?: string | null; checkouts?: RunCheckout[] } = {}) {
	const onactivate = vi.fn();
	const onclose = vi.fn();
	const onmove = vi.fn();
	const view = render(FileTabs, {
		tabs: three(),
		active: 'a',
		checkouts: [app],
		onactivate,
		onclose,
		onmove,
		...props
	});
	return { ...view, onactivate, onclose, onmove };
}

const tabs = () => screen.getAllByRole('tab');

describe('FileTabs', () => {
	describe('labels', () => {
		it('names a checkout file by its name on a single-checkout run, the path in its tooltip', () => {
			mount();
			const [a] = tabs();
			expect(a).toHaveAccessibleName('main.go');
			expect(a).toHaveTextContent('main.go');
			expect(a).toHaveAttribute('title', `src/main.go\n${HINT}`);
		});

		it('prefixes the checkout on a multi-checkout run, the primary by its name', () => {
			mount({ checkouts: [app, lib] });
			const [a, b] = tabs();
			expect(a).toHaveAccessibleName('app:main.go');
			expect(a).toHaveAttribute('title', `app:src/main.go\n${HINT}`);
			expect(b).toHaveAccessibleName('lib:b.ts');
			expect(b).toHaveAttribute('title', `lib:b.ts\n${HINT}`);
		});

		it('shows an absolute file by its whole path', () => {
			mount({ checkouts: [app, lib] });
			const c = tabs()[2];
			expect(c).toHaveAccessibleName('/etc/hosts');
			expect(c).toHaveAttribute('title', `/etc/hosts\n${HINT}`);
		});

		it('leaves the prefix empty when no checkout is the primary', () => {
			mount({ checkouts: [lib, { ...lib, name: 'other' }] });
			expect(tabs()[0]).toHaveAccessibleName(':main.go');
		});

		it('says which tab changed while hidden, with a dot', () => {
			mount({
				tabs: [tab('a', { repo: '', path: 'a.go' }, { stale: true }), tab('b', { abs: '/x' })]
			});
			expect(tabs()[0]).toHaveAccessibleName('a.go, changed');
			expect(tabs()[0]).toHaveTextContent('●');
			expect(tabs()[1]).toHaveAccessibleName('/x');
			expect(tabs()[1]).not.toHaveTextContent('●');
		});

		it('says which file is gone', () => {
			mount({ tabs: [tab('a', { repo: '', path: 'a.go' }, { deleted: true, stale: true })] });
			expect(tabs()[0]).toHaveAccessibleName('a.go, changed, deleted');
			expect(tabs()[0]).toHaveClass('gone');
		});
	});

	describe('selection', () => {
		it('marks the active tab, the one keyboard focus lands on', () => {
			mount({ active: 'b' });
			expect(tabs().map((t) => t.getAttribute('aria-selected'))).toEqual([
				'false',
				'true',
				'false'
			]);
			expect(tabs().map((t) => t.tabIndex)).toEqual([-1, 0, -1]);
		});

		it('lets focus land on the first tab when none is active', () => {
			mount({ active: null });
			expect(tabs().map((t) => t.tabIndex)).toEqual([0, -1, -1]);
		});

		it('activates a tab on a click', async () => {
			const { onactivate } = mount();
			await fireEvent.click(tabs()[1]);
			expect(onactivate).toHaveBeenCalledWith('b');
		});

		it.each(['Enter', ' '])('activates a tab on %j', async (key) => {
			const { onactivate } = mount();
			await fireEvent.keyDown(tabs()[2], { key });
			expect(onactivate).toHaveBeenCalledWith('c');
		});

		it.each([
			['ArrowRight', 0, 1],
			['ArrowRight', 2, 0],
			['ArrowLeft', 0, 2],
			['ArrowLeft', 2, 1],
			['Home', 2, 0],
			['End', 0, 2]
		])('%s from tab %i activates and focuses tab %i', async (key, from, to) => {
			const { onactivate, onmove } = mount();
			await fireEvent.keyDown(tabs()[from], { key });
			expect(onactivate).toHaveBeenCalledWith(['a', 'b', 'c'][to]);
			expect(tabs()[to]).toHaveFocus();
			expect(onmove).not.toHaveBeenCalled();
		});

		it('ignores other keys', async () => {
			const { onactivate, onmove, onclose } = mount();
			await fireEvent.keyDown(tabs()[0], { key: 'x' });
			await fireEvent.keyDown(tabs()[0], { key: 'ArrowUp', altKey: true, shiftKey: true });
			expect(onactivate).not.toHaveBeenCalled();
			expect(onmove).not.toHaveBeenCalled();
			expect(onclose).not.toHaveBeenCalled();
		});
	});

	describe('closing', () => {
		it('closes a tab on its ×, without activating it', async () => {
			const { onclose, onactivate } = mount();
			await fireEvent.click(screen.getByRole('button', { name: 'Close b.ts' }));
			expect(onclose).toHaveBeenCalledWith('b');
			expect(onactivate).not.toHaveBeenCalled();
		});

		it('keeps a key on the × from reaching the tab', async () => {
			const { onactivate } = mount();
			await fireEvent.keyDown(screen.getByRole('button', { name: 'Close b.ts' }), { key: 'Enter' });
			expect(onactivate).not.toHaveBeenCalled();
		});

		it('closes a tab on a middle click, and only on the middle button', async () => {
			const { onclose } = mount();
			await fireEvent(tabs()[2], new MouseEvent('auxclick', { bubbles: true, button: 2 }));
			expect(onclose).not.toHaveBeenCalled();
			await fireEvent(tabs()[2], new MouseEvent('auxclick', { bubbles: true, button: 1 }));
			expect(onclose).toHaveBeenCalledWith('c');
		});

		it('keeps a middle press from starting autoscroll', async () => {
			mount();
			expect(await fireEvent.mouseDown(tabs()[0], { button: 1 })).toBe(false);
			expect(await fireEvent.mouseDown(tabs()[0], { button: 0 })).toBe(true);
		});
	});

	describe('moving', () => {
		it('drags a tab forward, marking where it lands', async () => {
			const { onmove } = mount();
			const dataTransfer = { effectAllowed: '', setData: vi.fn() };
			await fireEvent.dragStart(tabs()[0], { dataTransfer });
			expect(dataTransfer.effectAllowed).toBe('move');
			expect(dataTransfer.setData).toHaveBeenCalledWith('text/plain', 'a');
			expect(tabs()[0]).toHaveClass('ghosted');
			expect(await fireEvent.dragOver(tabs()[2])).toBe(false);
			expect(tabs()[2]).toHaveClass('after');
			expect(tabs()[2]).not.toHaveClass('before');
			await fireEvent.drop(tabs()[2]);
			expect(onmove).toHaveBeenCalledWith(0, 2);
			expect(tabs()[0]).not.toHaveClass('ghosted');
			expect(tabs()[2]).not.toHaveClass('after');
		});

		it('drags a tab back', async () => {
			const { onmove } = mount();
			await fireEvent.dragStart(tabs()[2]);
			await fireEvent.dragOver(tabs()[0]);
			expect(tabs()[0]).toHaveClass('before');
			await fireEvent.drop(tabs()[0]);
			expect(onmove).toHaveBeenCalledWith(2, 0);
		});

		it('moves nothing when dropped where it started', async () => {
			const { onmove } = mount();
			await fireEvent.dragStart(tabs()[1]);
			await fireEvent.dragOver(tabs()[1]);
			expect(tabs()[1]).not.toHaveClass('before');
			expect(tabs()[1]).not.toHaveClass('after');
			await fireEvent.drop(tabs()[1]);
			expect(onmove).not.toHaveBeenCalled();
		});

		it('takes no drop that did not start on a tab', async () => {
			const { onmove } = mount();
			expect(await fireEvent.dragOver(tabs()[1])).toBe(true);
			await fireEvent.drop(tabs()[1]);
			expect(onmove).not.toHaveBeenCalled();
		});

		it('forgets a drag that ends elsewhere', async () => {
			mount();
			await fireEvent.dragStart(tabs()[0]);
			await fireEvent.dragOver(tabs()[1]);
			await fireEvent.dragEnd(tabs()[0]);
			expect(tabs()[0]).not.toHaveClass('ghosted');
			expect(tabs()[1]).not.toHaveClass('after');
		});

		it('moves a tab with Alt+Shift+arrows, keeping it focused', async () => {
			let order = three();
			const onmove = vi.fn((from: number, to: number) => {
				order = [...order];
				order.splice(to, 0, ...order.splice(from, 1));
				void view.rerender({ tabs: order });
			});
			const view = render(FileTabs, {
				tabs: order,
				active: 'a',
				checkouts: [app],
				onactivate: vi.fn(),
				onclose: vi.fn(),
				onmove
			});
			tabs()[0].focus();
			await fireEvent.keyDown(tabs()[0], { key: 'ArrowRight', altKey: true, shiftKey: true });
			expect(onmove).toHaveBeenCalledWith(0, 1);
			await waitFor(() => expect(tabs()[1]).toHaveFocus());
			expect(tabs()[1]).toHaveAccessibleName('main.go');
			await fireEvent.keyDown(tabs()[1], { key: 'ArrowLeft', altKey: true, shiftKey: true });
			expect(onmove).toHaveBeenLastCalledWith(1, 0);
			await waitFor(() => expect(tabs()[0]).toHaveFocus());
		});

		it('moves no tab past either end', async () => {
			const { onmove } = mount();
			await fireEvent.keyDown(tabs()[0], { key: 'ArrowLeft', altKey: true, shiftKey: true });
			await fireEvent.keyDown(tabs()[2], { key: 'ArrowRight', altKey: true, shiftKey: true });
			expect(onmove).not.toHaveBeenCalled();
		});
	});
});
