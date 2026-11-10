import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { rect } from '$test/helpers';
import Select, { type SelectOption } from './Select.svelte';

const options: SelectOption[] = [
	{ value: 'alpha', label: 'Alpha' },
	{ value: 'beta', label: 'Beta', detail: 'second letter' },
	{ value: 'bravo', label: 'Bravo', disabled: true },
	{ value: 'gamma', label: 'Gamma' }
];
const many: SelectOption[] = Array.from({ length: 11 }, (_, i) => ({
	value: `v${i}`,
	label: `Item ${i}`
}));
many[5] = { value: 'needle', label: 'Hay', detail: 'in the stack' };

const field = (name?: string) => screen.getByRole('combobox', { name });
const list = () => screen.getByRole('listbox');
const queryList = () => screen.queryByRole('listbox');
const option = (name: string | RegExp) => screen.getByRole('option', { name });
const shut = () => waitFor(() => expect(queryList()).toBeNull());
const filterBox = () => screen.getByRole('searchbox', { name: 'Filter…' });
const label = (row: Element | null) => row?.querySelector('span')?.textContent;
const labels = () => screen.getAllByRole('option').map(label);
/** The highlighted row's label, from what assistive tech is told. */
const highlighted = (from: HTMLElement = field()) =>
	label(document.getElementById(from.getAttribute('aria-activedescendant') ?? ''));

describe('Select', () => {
	describe('the field', () => {
		it('shows the chosen label, closed', () => {
			render(Select, { options, value: 'beta', label: 'Letter' });
			expect(field('Letter')).toHaveTextContent('Beta');
			expect(field('Letter')).toHaveAttribute('aria-expanded', 'false');
			expect(field('Letter')).toHaveAttribute('aria-haspopup', 'listbox');
			expect(field('Letter')).not.toHaveAttribute('aria-controls');
			expect(queryList()).toBeNull();
		});

		it('shows the placeholder while nothing is chosen', () => {
			render(Select, { options, value: undefined, placeholder: 'Pick one' });
			expect(field()).toHaveTextContent('Pick one');
		});

		it('shows the placeholder for a value that is not on the list', () => {
			render(Select, { options, value: 'gone', placeholder: 'Pick one' });
			expect(field()).toHaveTextContent('Pick one');
		});

		it('tips the chosen label unless given a title', async () => {
			const { rerender } = render(Select, { options, value: 'beta' });
			expect(field()).toHaveAttribute('title', 'Beta');
			await rerender({ title: 'The letter' });
			expect(field()).toHaveAttribute('title', 'The letter');
		});

		it('states invalid and required only when they hold', async () => {
			const { rerender } = render(Select, { options, value: 'beta' });
			expect(field()).not.toHaveAttribute('aria-invalid');
			expect(field()).not.toHaveAttribute('aria-required');

			await rerender({ invalid: true, required: true });
			expect(field()).toHaveAttribute('aria-invalid', 'true');
			expect(field()).toHaveAttribute('aria-required', 'true');
		});

		it('carries the id, so a label can point at it', () => {
			render(Select, { options, value: 'beta', id: 'letter' });
			expect(field()).toHaveAttribute('id', 'letter');
		});

		it('shows the dot of the chosen option', () => {
			render(Select, {
				options: [{ value: 'a', label: 'Agent', dot: 'running' as const }],
				value: 'a'
			});
			expect(field().querySelector('.hue-running')).not.toBeNull();
		});

		it('does not open while disabled', async () => {
			render(Select, { options, value: 'beta', disabled: true });
			await userEvent.click(field());
			expect(field()).toBeDisabled();
			expect(queryList()).toBeNull();
		});

		it('follows the value from outside', async () => {
			const { rerender } = render(Select, { options, value: 'beta' });
			await rerender({ value: 'gamma' });
			expect(field()).toHaveTextContent('Gamma');
		});
	});

	describe('the list', () => {
		it('opens on a click and closes on another', async () => {
			render(Select, { options, value: 'beta' });

			await userEvent.click(field());
			expect(list()).toBeInTheDocument();
			expect(field()).toHaveAttribute('aria-expanded', 'true');
			expect(field()).toHaveAttribute('aria-controls', list().id);

			await userEvent.click(field());
			await shut();
			expect(field()).toHaveAttribute('aria-expanded', 'false');
		});

		it('escapes the pane it sits in: the list lives under body', async () => {
			const { container } = render(Select, { options, value: 'beta' });
			await userEvent.click(field());
			expect(list().parentElement).toBe(document.body);
			expect(container).not.toContainElement(list());
		});

		it('marks the chosen row and the disabled one', async () => {
			render(Select, { options, value: 'beta' });
			await userEvent.click(field());

			expect(option(/Beta/)).toHaveAttribute('aria-selected', 'true');
			expect(option('Alpha')).toHaveAttribute('aria-selected', 'false');
			expect(option('Bravo')).toHaveAttribute('aria-disabled', 'true');
			expect(option('Alpha')).not.toHaveAttribute('aria-disabled');
		});

		it('shows a detail on its row', async () => {
			render(Select, { options, value: 'alpha' });
			await userEvent.click(field());
			expect(within(option(/Beta/)).getByText('second letter')).toBeInTheDocument();
		});

		it('picks a row, tells the caller, closes and returns the focus', async () => {
			const onchange = vi.fn();
			render(Select, { options, value: 'alpha', onchange });
			await userEvent.click(field());

			await userEvent.click(option('Gamma'));

			await shut();
			expect(onchange).toHaveBeenCalledExactlyOnceWith('gamma');
			expect(field()).toHaveTextContent('Gamma');
			expect(field()).toHaveFocus();
		});

		it('closes without a change when the chosen row is picked again', async () => {
			const onchange = vi.fn();
			render(Select, { options, value: 'alpha', onchange });
			await userEvent.click(field());

			await userEvent.click(option('Alpha'));

			await shut();
			expect(onchange).not.toHaveBeenCalled();
		});

		it('ignores a click on a disabled row, and stays open', async () => {
			const onchange = vi.fn();
			render(Select, { options, value: 'alpha', onchange });
			await userEvent.click(field());

			await userEvent.click(option('Bravo'));

			expect(list()).toBeInTheDocument();
			expect(onchange).not.toHaveBeenCalled();
		});

		it('closes on a press outside, keeping the value', async () => {
			render(Select, { options, value: 'alpha' });
			await userEvent.click(field());

			await userEvent.pointer({ target: document.body, keys: '[MouseLeft]' });

			await shut();
			expect(field()).toHaveTextContent('Alpha');
			expect(field()).toHaveAttribute('aria-expanded', 'false');
		});

		it('keeps the focus on the field while a row is pressed', async () => {
			render(Select, { options, value: 'alpha' });
			await userEvent.click(field());
			expect(await fireEvent.mouseDown(option('Gamma'))).toBe(false);
		});

		it('highlights the row under the pointer, but never a disabled one', async () => {
			render(Select, { options, value: 'alpha' });
			await userEvent.click(field());

			await fireEvent.pointerMove(option('Gamma'));
			expect(highlighted()).toBe('Gamma');

			await fireEvent.pointerMove(option('Bravo'));
			expect(highlighted()).toBe('Gamma');
		});
	});

	describe('groups', () => {
		const grouped: SelectOption[] = [
			{ value: 'f1', label: 'Mine', group: 'Project flows' },
			{ value: 't1', label: 'Builder', group: 'Built-in templates' },
			{ value: 'f2', label: 'Other', group: 'Project flows' }
		];

		it('lists options under their heading, in order of first appearance', async () => {
			render(Select, { options: grouped, value: 'f1' });
			await userEvent.click(field());

			expect(labels()).toEqual(['Mine', 'Other', 'Builder']);
			const text = list().textContent ?? '';
			expect(text.indexOf('Project flows')).toBeLessThan(text.indexOf('Mine'));
			expect(text.indexOf('Other')).toBeLessThan(text.indexOf('Built-in templates'));
			expect(text.indexOf('Built-in templates')).toBeLessThan(text.indexOf('Builder'));
		});

		it('drops a heading whose options are all filtered out', async () => {
			render(Select, { options: grouped, value: 'f1', searchable: true });
			await userEvent.click(field());

			await userEvent.type(filterBox(), 'builder');

			expect(screen.queryByText('Project flows')).toBeNull();
			expect(screen.getByText('Built-in templates')).toBeInTheDocument();
		});

		// A group split across the caller's list: Mine, Builder, Other.
		it('walks the rows in the order they are drawn', async () => {
			render(Select, { options: grouped, value: 'f1' });
			field().focus();
			await userEvent.keyboard('{ArrowDown}{ArrowDown}');

			expect(highlighted()).toBe('Other');
		});

		it('opens on the chosen row where it is drawn', async () => {
			render(Select, { options: grouped, value: 't1' });
			field().focus();
			await userEvent.keyboard('{Enter}{ArrowUp}');

			expect(highlighted()).toBe('Other');
		});

		it('type-aheads from the chosen row where it is drawn, closed', async () => {
			const onchange = vi.fn();
			render(Select, {
				options: [
					{ value: 'a1', label: 'Able', group: 'One' },
					{ value: 'b1', label: 'Baker', group: 'Two' },
					{ value: 'a2', label: 'Acorn', group: 'One' }
				],
				value: 'a2',
				onchange
			});
			field().focus();

			await userEvent.keyboard('a');

			expect(onchange).toHaveBeenCalledWith('a1');
		});
	});

	describe('keyboard', () => {
		it.each(['{ArrowDown}', '{ArrowUp}', '{Enter}', ' '])('opens with "%s"', async (key) => {
			render(Select, { options, value: 'beta' });
			field().focus();

			await userEvent.keyboard(key);

			expect(list()).toBeInTheDocument();
			expect(highlighted()).toBe('Beta');
		});

		it('opens on the first enabled row when nothing is chosen', async () => {
			render(Select, {
				options: [{ value: 'x', label: 'X', disabled: true }, ...options],
				value: undefined
			});
			field().focus();
			await userEvent.keyboard('{Enter}');
			expect(highlighted()).toBe('Alpha');
		});

		it('moves down and up, skipping disabled rows', async () => {
			render(Select, { options, value: 'beta' });
			field().focus();
			await userEvent.keyboard('{Enter}{ArrowDown}');
			expect(highlighted()).toBe('Gamma');

			await userEvent.keyboard('{ArrowUp}');
			expect(highlighted()).toBe('Beta');
		});

		it('wraps at both ends', async () => {
			render(Select, { options, value: 'gamma' });
			field().focus();
			await userEvent.keyboard('{Enter}{ArrowDown}');
			expect(highlighted()).toBe('Alpha');

			await userEvent.keyboard('{ArrowUp}');
			expect(highlighted()).toBe('Gamma');
		});

		it('goes to the first and last enabled rows with Home and End', async () => {
			render(Select, {
				options: [
					{ value: 'x', label: 'X', disabled: true },
					...options,
					{ value: 'z', label: 'Z', disabled: true }
				],
				value: 'beta'
			});
			field().focus();
			await userEvent.keyboard('{Enter}{Home}');
			expect(highlighted()).toBe('Alpha');

			await userEvent.keyboard('{End}');
			expect(highlighted()).toBe('Gamma');
		});

		it('keeps the highlighted row in view', async () => {
			const scroll = vi.spyOn(Element.prototype, 'scrollIntoView');
			render(Select, { options, value: 'alpha' });
			field().focus();
			await userEvent.keyboard('{Enter}{ArrowDown}');

			expect(scroll).toHaveBeenLastCalledWith({ block: 'nearest' });
			expect(scroll.mock.contexts.at(-1)).toBe(option(/Beta/));
		});

		it.each(['{Enter}', ' '])('picks the highlighted row with "%s"', async (key) => {
			const onchange = vi.fn();
			render(Select, { options, value: 'alpha', onchange });
			field().focus();
			await userEvent.keyboard('{Enter}{End}');

			await userEvent.keyboard(key);

			await shut();
			expect(onchange).toHaveBeenCalledExactlyOnceWith('gamma');
			expect(field()).toHaveFocus();
		});

		it('leaves Home and End to the page while closed', async () => {
			render(Select, { options, value: 'beta' });
			field().focus();
			await userEvent.keyboard('{Home}{End}');
			expect(queryList()).toBeNull();
			expect(field()).toHaveTextContent('Beta');
		});

		it('closes on Esc, keeping the value and the focus', async () => {
			const onchange = vi.fn();
			render(Select, { options, value: 'alpha', onchange });
			field().focus();
			await userEvent.keyboard('{Enter}{ArrowDown}{Escape}');

			await shut();
			expect(onchange).not.toHaveBeenCalled();
			expect(field()).toHaveFocus();
			expect(field()).toHaveTextContent('Alpha');
		});

		it('closes on Tab and lets the focus move on', async () => {
			render(Select, { options, value: 'alpha' });
			const next = document.body.appendChild(document.createElement('button'));
			field().focus();

			await userEvent.keyboard('{Enter}');
			await userEvent.tab();

			await shut();
			expect(next).toHaveFocus();
			next.remove();
		});

		it('points assistive tech at the highlighted row only while open', async () => {
			render(Select, { options, value: 'alpha' });
			expect(field()).not.toHaveAttribute('aria-activedescendant');
			await userEvent.click(field());
			expect(field().getAttribute('aria-activedescendant')).toBe(option('Alpha').id);
		});

		it('does nothing with no options at all', async () => {
			render(Select, { options: [], value: undefined });
			field().focus();
			await userEvent.keyboard('{Enter}{ArrowDown}{Enter}a');
			expect(screen.getByText('Nothing matches')).toBeInTheDocument();
		});

		it('picks nothing when every option is disabled', async () => {
			const onchange = vi.fn();
			render(Select, {
				options: [{ value: 'x', label: 'X', disabled: true }],
				value: undefined,
				onchange
			});
			field().focus();
			await userEvent.keyboard('{Enter}{ArrowDown}{Enter}');
			expect(onchange).not.toHaveBeenCalled();
		});
	});

	describe('type-ahead', () => {
		afterEach(() => {
			vi.useRealTimers();
		});

		it('changes the value straight away while closed, as a native select does', async () => {
			const onchange = vi.fn();
			render(Select, { options, value: 'alpha', onchange });
			field().focus();

			await userEvent.keyboard('g');

			expect(onchange).toHaveBeenCalledExactlyOnceWith('gamma');
			expect(queryList()).toBeNull();
		});

		it('moves the highlight while open, changing nothing yet', async () => {
			const onchange = vi.fn();
			render(Select, { options, value: 'alpha', onchange });
			field().focus();

			await userEvent.keyboard('{Enter}g');

			expect(highlighted()).toBe('Gamma');
			expect(onchange).not.toHaveBeenCalled();
		});

		it('builds a prefix from letters typed quickly', async () => {
			vi.useFakeTimers({ toFake: ['Date'], now: 1_000 });
			render(Select, {
				options: [
					{ value: 'a', label: 'Alpha' },
					{ value: 'g', label: 'Gamma' },
					{ value: 'ga', label: 'Gate' }
				],
				value: 'a'
			});
			field().focus();

			await userEvent.keyboard('{Enter}gat');

			expect(highlighted()).toBe('Gate');
		});

		it('starts a new prefix after half a second', async () => {
			vi.useFakeTimers({ toFake: ['Date'], now: 1_000 });
			render(Select, { options, value: 'alpha' });
			field().focus();
			await userEvent.keyboard('{Enter}g');

			vi.setSystemTime(1_501);
			await userEvent.keyboard('a');

			expect(highlighted()).toBe('Alpha');
		});

		it('moves past the current row on a repeated first letter', async () => {
			vi.useFakeTimers({ toFake: ['Date'], now: 1_000 });
			render(Select, {
				options: [
					{ value: 'b1', label: 'Beta' },
					{ value: 'b2', label: 'Bravo' }
				],
				value: 'b1'
			});
			field().focus();
			await userEvent.keyboard('{Enter}b');

			expect(highlighted()).toBe('Bravo');
		});

		it('skips disabled rows', async () => {
			vi.useFakeTimers({ toFake: ['Date'], now: 1_000 });
			render(Select, { options, value: 'gamma' });
			field().focus();
			await userEvent.keyboard('{Enter}br');
			expect(highlighted()).toBe('Beta');
		});

		it('ignores shortcuts', async () => {
			const onchange = vi.fn();
			render(Select, { options, value: 'alpha', onchange });
			field().focus();

			await userEvent.keyboard('{Control>}g{/Control}{Meta>}g{/Meta}{Alt>}g{/Alt}');

			expect(onchange).not.toHaveBeenCalled();
		});
	});

	describe('filter', () => {
		it('appears on a long list', async () => {
			render(Select, { options: many, value: 'v0' });
			await userEvent.click(field());
			expect(filterBox()).toBeInTheDocument();
		});

		it('stays away from a list of ten', async () => {
			render(Select, { options: many.slice(0, 10), value: 'v0' });
			await userEvent.click(field());
			expect(screen.queryByRole('searchbox')).toBeNull();
		});

		it('obeys searchable, either way', async () => {
			const off = render(Select, { options: many, value: 'v0', searchable: false });
			await userEvent.click(field());
			expect(screen.queryByRole('searchbox')).toBeNull();
			off.unmount();

			render(Select, { options, value: 'alpha', searchable: true });
			await userEvent.click(field());
			expect(filterBox()).toBeInTheDocument();
		});

		it('takes the focus when the list opens', async () => {
			render(Select, { options: many, value: 'v0' });
			await userEvent.click(field());
			await waitFor(() => expect(filterBox()).toHaveFocus());
		});

		// The filter takes the focus a frame after opening. A list closed
		// within that frame keeps the focus on the field: the late frame must
		// not send it into the filter box of a list on its way out.
		it('leaves the focus on the field when the list closes within a frame', async () => {
			const frames: FrameRequestCallback[] = [];
			const raf = vi
				.spyOn(window, 'requestAnimationFrame')
				.mockImplementation((cb) => frames.push(cb));
			render(Select, { options: many, value: 'v0' });
			await userEvent.click(field());
			await userEvent.keyboard('{Escape}');
			expect(field()).toHaveFocus();

			frames.forEach((cb) => cb(0));
			raf.mockRestore();

			expect(field()).toHaveFocus();
		});

		it.each([
			['the label', 'hay'],
			['the detail', 'STACK'],
			['the value', 'needle']
		])('matches on %s, ignoring case', async (_, query) => {
			render(Select, { options: many, value: 'v0' });
			await userEvent.click(field());

			await userEvent.type(filterBox(), query);

			expect(labels()).toEqual(['Hay']);
		});

		it('says so when nothing matches', async () => {
			render(Select, { options: many, value: 'v0' });
			await userEvent.click(field());

			await userEvent.type(filterBox(), 'zzz');

			expect(screen.queryByRole('option')).toBeNull();
			expect(screen.getByText('Nothing matches')).toBeInTheDocument();
		});

		it('keeps a row highlighted as the list narrows', async () => {
			render(Select, { options: many, value: 'v9' });
			await userEvent.click(field());

			await userEvent.type(filterBox(), 'hay');

			expect(highlighted(filterBox())).toBe('Hay');
		});

		it('moves the highlight from the filter field and picks with Enter', async () => {
			const onchange = vi.fn();
			render(Select, { options: many, value: 'v0', onchange });
			await userEvent.click(field());
			await waitFor(() => expect(filterBox()).toHaveFocus());

			await userEvent.keyboard('{ArrowDown}{ArrowDown}{Enter}');

			await shut();
			expect(onchange).toHaveBeenCalledExactlyOnceWith('v2');
			expect(field()).toHaveFocus();
		});

		it('points assistive tech from the filter, not the field', async () => {
			render(Select, { options: many, value: 'v0' });
			await userEvent.click(field());
			expect(filterBox()).toHaveAttribute('aria-activedescendant', option('Item 0').id);
			expect(filterBox()).toHaveAttribute('aria-controls', list().id);
			expect(field()).not.toHaveAttribute('aria-activedescendant');
		});

		it('types a space and goes Home and End within the text', async () => {
			render(Select, { options: many, value: 'v0' });
			await userEvent.click(field());
			await waitFor(() => expect(filterBox()).toHaveFocus());

			await userEvent.keyboard('item 1{Home}{End}');

			expect(filterBox()).toHaveValue('item 1');
			expect(queryList()).not.toBeNull();
		});

		it('closes on Esc with the focus back on the field', async () => {
			render(Select, { options: many, value: 'v0' });
			await userEvent.click(field());
			await waitFor(() => expect(filterBox()).toHaveFocus());

			await userEvent.keyboard('{Escape}');

			await shut();
			expect(field()).toHaveFocus();
		});

		it('starts empty the next time it opens', async () => {
			render(Select, { options: many, value: 'v0' });
			await userEvent.click(field());
			await userEvent.type(filterBox(), 'hay{Enter}');
			await shut();

			await userEvent.click(field());

			expect(filterBox()).toHaveValue('');
			expect(screen.getAllByRole('option')).toHaveLength(many.length);
		});

		// The closed field type-aheads through `shown`: under the old filter, 'i'
		// would find nothing, though Item 0 is right there.
		it('forgets the filter for type-ahead once closed', async () => {
			const onchange = vi.fn();
			render(Select, { options: many, value: 'v0', onchange });
			await userEvent.click(field());
			await userEvent.type(filterBox(), 'hay{Enter}');
			await shut();
			onchange.mockClear();

			await userEvent.keyboard('i');

			expect(onchange).toHaveBeenCalledOnce();
		});
	});

	describe('in a form', () => {
		const form = () => document.body.appendChild(document.createElement('form'));

		afterEach(() => {
			document.querySelectorAll('form').forEach((f) => f.remove());
		});

		it('submits its value under its name', () => {
			const target = form();
			render(Select, { target, props: { options, value: 'beta', name: 'letter' } });
			expect(new FormData(target).get('letter')).toBe('beta');
		});

		it('has no form part without a name or required', () => {
			const target = form();
			render(Select, { target, props: { options, value: 'beta' } });
			expect(target.querySelector('input')).toBeNull();
		});

		it('blocks the submit while required and empty', async () => {
			const target = form();
			const { rerender } = render(Select, {
				target,
				props: { options, value: undefined, required: true }
			});
			expect(target.checkValidity()).toBe(false);

			await rerender({ value: 'beta' });
			expect(target.checkValidity()).toBe(true);
		});

		it('hands the focus to the field when the browser points at it', () => {
			const target = form();
			render(Select, { target, props: { options, value: undefined, required: true } });

			target.querySelector('input')!.focus();

			expect(field()).toHaveFocus();
		});

		it('keeps its form part out of the tab order and the a11y tree', () => {
			const target = form();
			render(Select, { target, props: { options, value: 'beta', name: 'letter' } });
			const proxy = target.querySelector('input')!;
			expect(proxy).toHaveAttribute('tabindex', '-1');
			expect(proxy).toHaveAttribute('aria-hidden', 'true');
		});
	});

	// The field sits low on the page, so the list has to open upward: the path
	// that used to loop to effect_update_depth_exceeded.
	describe('near the bottom of the window', () => {
		function low() {
			vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue(rect(10, 700, 100, 30));
			vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockReturnValue(300);
		}

		it('opens upward at the field width', async () => {
			low();
			render(Select, { options, value: 'alpha' });

			await userEvent.click(field());
			await tick();

			const { top, bottom, minWidth, maxHeight } = list().style;
			expect({ top, bottom, minWidth, maxHeight }).toEqual({
				top: '',
				bottom: '74px',
				minWidth: '100px',
				maxHeight: '686px'
			});
		});

		it('still picks after flipping', async () => {
			low();
			const onchange = vi.fn();
			render(Select, { options, value: 'alpha', onchange });

			await userEvent.click(field());
			await tick();
			await userEvent.click(option('Gamma'));

			await shut();
			expect(onchange).toHaveBeenCalledExactlyOnceWith('gamma');
		});

		it('reopens and closes over and over without running away', async () => {
			low();
			render(Select, { options, value: 'alpha' });

			for (let i = 0; i < 3; i++) {
				await userEvent.click(field());
				await tick();
				expect(list().style.bottom).toBe('74px');
				await userEvent.click(field());
				await shut();
			}
		});
	});
});
