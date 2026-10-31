import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick, type ComponentProps } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { rect, text } from '$test/helpers';
import Popover from './Popover.svelte';

// jsdom: the viewport is 1024×768 and nothing has a size of its own.
const anchors: HTMLElement[] = [];
function anAnchor(box = rect(100, 200, 80, 30)) {
	const a = document.body.appendChild(document.createElement('button'));
	a.textContent = 'Env';
	vi.spyOn(a, 'getBoundingClientRect').mockReturnValue(box);
	anchors.push(a);
	return a;
}
const listHeight = (px: number) =>
	vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockReturnValue(px);
const pop = () => screen.getByRole('dialog');
const queryPop = () => screen.queryByRole('dialog');
const gone = () => waitFor(() => expect(queryPop()).toBeNull());
const at = () => {
	const { left, top, right, bottom, maxHeight } = pop().style;
	return { left, top, right, bottom, maxHeight };
};
type Props = Partial<ComponentProps<typeof Popover>>;
// `anchor` is also a mount option, so the props can't go in the short form.
const show = (props: Props = {}) =>
	render(Popover, { props: { open: true, children: text('Body'), ...props } });
/** Closed first: the materialise only plays on an open after mount. */
async function opening(props: Props) {
	const shown = show({ ...props, open: false });
	await shown.rerender({ open: true });
	await tick();
	return shown;
}

describe('Popover', () => {
	afterEach(() => {
		anchors.splice(0).forEach((a) => a.remove());
	});

	it('shows its content only while open', async () => {
		const { rerender } = show({ open: false });
		expect(queryPop()).toBeNull();

		await rerender({ open: true });
		expect(pop()).toHaveTextContent('Body');

		await rerender({ open: false });
		await gone();
	});

	it('takes the role, id and width it is given', () => {
		show({ role: 'menu', id: 'env-menu', width: 240 });
		const menu = screen.getByRole('menu');
		expect(menu).toHaveAttribute('id', 'env-menu');
		expect(menu.style.width).toBe('240px');
	});

	it('titles itself, with a legend beside the title', () => {
		show({ title: 'Environment', legend: 'live' });
		expect(screen.getByText('Environment')).toBeInTheDocument();
		expect(screen.getByText('live')).toBeInTheDocument();
	});

	it('drops the legend without a title', () => {
		show({ legend: 'live' });
		expect(screen.queryByText('live')).toBeNull();
	});

	describe('position', () => {
		it.each([
			['below-start', { left: '100px', top: '236px' }, 'left top'],
			['below-end', { right: '844px', top: '236px' }, 'right top'],
			['above-start', { left: '100px', bottom: '574px' }, 'left bottom'],
			['above-end', { right: '844px', bottom: '574px' }, 'right bottom'],
			['right', { left: '190px', top: '200px' }, 'left top']
		] as const)('opens %s beside its anchor, grown from it', async (placement, where, origin) => {
			await opening({ anchor: anAnchor(), placement });
			expect(at()).toMatchObject({ left: '', top: '', right: '', bottom: '', ...where });
			expect(pop().style.transformOrigin).toBe(origin);
		});

		it.each([
			['below-start', rect(-40, 200, 30, 30), 'left'],
			['below-end', rect(1000, 200, 200, 30), 'right'],
			['right', rect(100, -10, 80, 30), 'top']
		] as const)('keeps %s eight pixels inside the viewport', (placement, box, side) => {
			show({ anchor: anAnchor(box), placement });
			expect(at()[side]).toBe('8px');
		});

		it('goes where `at` says, over its anchor', async () => {
			await opening({ anchor: anAnchor(), at: { left: 11, top: 22 } });
			expect(at()).toMatchObject({ left: '11px', top: '22px', right: '', bottom: '' });
			expect(pop().style.transformOrigin).toBe('left top');
		});

		it('is at least as wide as its anchor with matchWidth', () => {
			show({ anchor: anAnchor(), matchWidth: true });
			expect(pop().style.minWidth).toBe('80px');
		});

		it('has no minimum width of its own otherwise', () => {
			show({ anchor: anAnchor() });
			expect(pop().style.minWidth).toBe('');
		});

		it('follows its anchor when the window resizes', async () => {
			const anchor = anAnchor();
			show({ anchor });

			vi.mocked(anchor.getBoundingClientRect).mockReturnValue(rect(300, 400, 80, 30));
			window.dispatchEvent(new Event('resize'));
			await tick();

			expect(at()).toMatchObject({ left: '300px', top: '436px' });
		});

		it('follows its anchor on a scroll only when portaled', async () => {
			const anchor = anAnchor();
			const { rerender } = show({ anchor, portal: true });

			vi.mocked(anchor.getBoundingClientRect).mockReturnValue(rect(300, 400, 80, 30));
			document.body.dispatchEvent(new Event('scroll'));
			await tick();
			expect(at()).toMatchObject({ left: '300px', top: '436px' });

			await rerender({ portal: false });
			vi.mocked(anchor.getBoundingClientRect).mockReturnValue(rect(1, 2, 80, 30));
			document.body.dispatchEvent(new Event('scroll'));
			await tick();
			expect(at()).toMatchObject({ left: '300px', top: '436px' });
		});

		it('stops measuring once closed', async () => {
			const anchor = anAnchor();
			const { rerender } = show({ anchor });
			await rerender({ open: false });

			vi.mocked(anchor.getBoundingClientRect).mockClear();
			window.dispatchEvent(new Event('resize'));

			expect(anchor.getBoundingClientRect).not.toHaveBeenCalled();
		});
	});

	// A flip used to subscribe place()'s effect to its own write and spin to
	// effect_update_depth_exceeded, which is what the render in each of these
	// would throw. The geometry below is enough to take the flipping path.
	describe('flip', () => {
		const low = rect(10, 700, 100, 30); // 24px free below, 686px above
		const high = rect(10, 20, 100, 30); // 704px free below, 6px above

		it('opens upward when the list is short of room below and there is more above', async () => {
			listHeight(300);
			show({ anchor: anAnchor(low), flip: true });
			await tick();

			expect(at()).toEqual({
				left: '10px',
				top: '',
				right: '',
				bottom: '74px',
				maxHeight: '686px'
			});
		});

		it('keeps the end alignment through the flip', async () => {
			listHeight(300);
			show({ anchor: anAnchor(low), flip: true, placement: 'below-end' });
			await tick();

			expect(at()).toMatchObject({ left: '', right: '914px', bottom: '74px' });
		});

		it('opens downward from an above placement with no room up there', async () => {
			listHeight(300);
			show({ anchor: anAnchor(high), flip: true, placement: 'above-start' });
			await tick();

			expect(at()).toMatchObject({ top: '56px', bottom: '', maxHeight: '704px' });
		});

		it('stays put when the list fits, capped to the room there is', async () => {
			listHeight(20);
			show({ anchor: anAnchor(low), flip: true });
			await tick();

			expect(at()).toMatchObject({ top: '736px', bottom: '', maxHeight: '24px' });
		});

		it('stays put when the other side has less room still', async () => {
			listHeight(300);
			show({ anchor: anAnchor(rect(10, 370, 100, 30)), flip: true });
			await tick();

			expect(at()).toMatchObject({ top: '406px', bottom: '', maxHeight: '354px' });
		});

		it('never flips without being asked', async () => {
			listHeight(300);
			show({ anchor: anAnchor(low) });
			await tick();

			expect(at()).toMatchObject({ top: '736px', bottom: '', maxHeight: '' });
		});

		it('leaves a right-hand popover where it is', async () => {
			listHeight(300);
			show({ anchor: anAnchor(low), flip: true, placement: 'right' });
			await tick();

			expect(at()).toMatchObject({ left: '120px', top: '700px', maxHeight: '' });
		});

		it('decides afresh each time it opens', async () => {
			listHeight(300);
			const anchor = anAnchor(low);
			const { rerender } = show({ anchor, flip: true });
			await tick();
			expect(at()).toMatchObject({ bottom: '74px' });

			await rerender({ open: false });
			await gone();
			vi.mocked(anchor.getBoundingClientRect).mockReturnValue(high);
			await rerender({ open: true });
			await tick();

			expect(at()).toMatchObject({ top: '56px', bottom: '' });
		});

		it('settles on the flipped side through a resize', async () => {
			listHeight(300);
			const anchor = anAnchor(low);
			show({ anchor, flip: true });
			await tick();

			window.dispatchEvent(new Event('resize'));
			await tick();

			expect(at()).toMatchObject({ bottom: '74px' });
		});

		// materialize takes its origin before place() has measured the list.
		it('grows out of the anchor on the side it flipped to', async () => {
			listHeight(300);
			await opening({ anchor: anAnchor(low), flip: true });

			expect(pop().style.transformOrigin).toBe('left bottom');
		});
	});

	describe('closing', () => {
		it('closes on a press outside and calls onclose', async () => {
			const onclose = vi.fn();
			show({ anchor: anAnchor(), onclose });

			await userEvent.pointer({ target: document.body, keys: '[MouseLeft]' });

			await gone();
			expect(onclose).toHaveBeenCalledOnce();
		});

		it('stays open on a press inside', async () => {
			const onclose = vi.fn();
			show({ anchor: anAnchor(), onclose });

			await userEvent.click(screen.getByText('Body'));

			expect(pop()).toBeInTheDocument();
			expect(onclose).not.toHaveBeenCalled();
		});

		it('leaves a press on its anchor to the anchor: that is its toggle', async () => {
			const onclose = vi.fn();
			const anchor = anAnchor();
			show({ anchor, onclose });

			await userEvent.click(anchor);

			expect(pop()).toBeInTheDocument();
			expect(onclose).not.toHaveBeenCalled();
		});

		it('stays open on a press outside when sticky, but Esc still closes it', async () => {
			const onclose = vi.fn();
			show({ anchor: anAnchor(), sticky: true, onclose });

			await userEvent.pointer({ target: document.body, keys: '[MouseLeft]' });
			expect(onclose).not.toHaveBeenCalled();

			await userEvent.keyboard('{Escape}');
			await gone();
			expect(onclose).toHaveBeenCalledOnce();
		});

		it('closes on Esc and gives the focus back to its anchor', async () => {
			const onclose = vi.fn();
			const anchor = anAnchor();
			show({ anchor, onclose });

			await userEvent.keyboard('{Escape}');

			await gone();
			expect(onclose).toHaveBeenCalledOnce();
			expect(anchor).toHaveFocus();
		});

		it('takes other keys as nothing', async () => {
			show({ anchor: anAnchor() });
			await userEvent.keyboard('x{Enter}');
			expect(pop()).toBeInTheDocument();
		});

		it('leaves Esc alone when something inside already took it', async () => {
			const onclose = vi.fn();
			const taken = (e: KeyboardEvent) => e.preventDefault();
			document.addEventListener('keydown', taken);
			show({ anchor: anAnchor(), onclose });

			await userEvent.keyboard('{Escape}');
			document.removeEventListener('keydown', taken);

			expect(onclose).not.toHaveBeenCalled();
		});

		it('closes only the top one on Esc, then the one under it', async () => {
			const first = vi.fn();
			const second = vi.fn();
			show({ anchor: anAnchor(), sticky: true, onclose: first });
			show({ anchor: anAnchor(), sticky: true, onclose: second });

			await userEvent.keyboard('{Escape}');
			expect(second).toHaveBeenCalledOnce();
			expect(first).not.toHaveBeenCalled();

			await userEvent.keyboard('{Escape}');
			expect(first).toHaveBeenCalledOnce();
		});

		it('stops listening once closed', async () => {
			const onclose = vi.fn();
			const { rerender } = show({ anchor: anAnchor(), onclose });

			await rerender({ open: false });
			await userEvent.keyboard('{Escape}');
			await userEvent.pointer({ target: document.body, keys: '[MouseLeft]' });

			expect(onclose).not.toHaveBeenCalled();
		});

		it('gives way on the stack when unmounted while open', async () => {
			const under = vi.fn();
			show({ anchor: anAnchor(), sticky: true, onclose: under });
			const top = show({ anchor: anAnchor(), sticky: true });

			top.unmount();
			await userEvent.keyboard('{Escape}');

			expect(under).toHaveBeenCalledOnce();
		});
	});

	describe('portal', () => {
		it('stays where it was put by default', () => {
			const { container } = show();
			expect(container).toContainElement(pop());
		});

		it('moves under body with portal, and takes itself away after', async () => {
			const { container, rerender } = show({ portal: true });
			expect(pop().parentElement).toBe(document.body);
			expect(container).not.toContainElement(pop());

			await rerender({ open: false });
			await gone();
			expect(document.body.querySelector('.pop')).toBeNull();
		});
	});
});
