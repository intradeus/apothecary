import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import Select from './Select.svelte';
import { trap, type TrapOptions } from './trap';

function aDialog(html: string) {
	const node = document.body.appendChild(document.createElement('div'));
	node.innerHTML = html;
	return node;
}
const button = (name: string) => screen.getByRole('button', { name });
// A trap listens on window, so one left alive would act on every later test.
const live = new Set<{ destroy?: () => void }>();
/** The action, plus the microtask its first focus waits for. */
async function trapped(node: HTMLElement, opts: Partial<TrapOptions> = {}) {
	const action = trap(node, { onescape: vi.fn(), ...opts }) as {
		update?: (next: TrapOptions) => void;
		destroy?: () => void;
	};
	live.add(action);
	await Promise.resolve();
	return action;
}

describe('trap', () => {
	let opener: HTMLButtonElement;

	beforeEach(() => {
		opener = document.body.appendChild(document.createElement('button'));
		opener.textContent = 'Open';
		opener.focus();
	});
	afterEach(() => {
		live.forEach((action) => action.destroy?.());
		live.clear();
		document.body.replaceChildren();
	});

	describe('first focus', () => {
		it('goes to the first field', async () => {
			const node = aDialog(
				'<button>Cancel</button><input aria-label="Name" /><textarea></textarea>'
			);
			await trapped(node);
			expect(screen.getByRole('textbox', { name: 'Name' })).toHaveFocus();
		});

		it('goes to the last button when there is no field: the action, not Cancel', async () => {
			const node = aDialog('<button>Cancel</button><button>Delete</button>');
			await trapped(node);
			expect(button('Delete')).toHaveFocus();
		});

		it('skips a disabled last button', async () => {
			const node = aDialog('<button>Cancel</button><button disabled>Delete</button>');
			await trapped(node);
			expect(button('Cancel')).toHaveFocus();
		});

		it('goes where `initial` says', async () => {
			const node = aDialog('<input /><button>Cancel</button><button>Delete</button>');
			await trapped(node, { initial: () => button('Cancel') });
			expect(button('Cancel')).toHaveFocus();
		});

		it('falls back when `initial` finds nothing', async () => {
			const node = aDialog('<button>Cancel</button><button>Delete</button>');
			await trapped(node, { initial: () => null });
			expect(button('Delete')).toHaveFocus();
		});

		it('stays calm with nothing to focus', async () => {
			await expect(trapped(aDialog('<p>Nothing here</p>'))).resolves.toBeDefined();
			expect(opener).toHaveFocus();
		});
	});

	describe('Tab', () => {
		const three = '<button>One</button><button>Two</button><button>Three</button>';

		it('wraps from the last to the first', async () => {
			await trapped(aDialog(three));
			button('Three').focus();
			await userEvent.tab();
			expect(button('One')).toHaveFocus();
		});

		it('wraps from the first to the last going back', async () => {
			await trapped(aDialog(three));
			button('One').focus();
			await userEvent.tab({ shift: true });
			expect(button('Three')).toHaveFocus();
		});

		it('moves normally in the middle', async () => {
			await trapped(aDialog(three));
			button('One').focus();
			await userEvent.tab();
			expect(button('Two')).toHaveFocus();
		});

		it('pulls the focus back in from outside', async () => {
			await trapped(aDialog(three));
			// After the dialog: from before it, a plain Tab would land on One anyway.
			const after = document.body.appendChild(document.createElement('button'));
			after.focus();
			await userEvent.tab();
			expect(button('One')).toHaveFocus();
		});

		it('pulls it back to the end going backwards from outside', async () => {
			await trapped(aDialog(three));
			opener.focus();
			await userEvent.tab({ shift: true });
			expect(button('Three')).toHaveFocus();
		});

		it('treats a disabled button as not there', async () => {
			await trapped(
				aDialog('<button>One</button><button>Two</button><button disabled>No</button>')
			);
			button('Two').focus();
			await userEvent.tab();
			expect(button('One')).toHaveFocus();
		});

		it('counts links and tabindex stops, but not tabindex -1 on a plain element', async () => {
			const node = aDialog(
				'<a href="/x">Link</a><div tabindex="0">Stop</div><div tabindex="-1">Skip</div>'
			);
			await trapped(node);
			screen.getByText('Stop').focus();
			await userEvent.tab();
			expect(screen.getByRole('link', { name: 'Link' })).toHaveFocus();
		});

		// Roving options and Select's proxy input: seen as the last stop, they let
		// Tab walk out of the dialog from the real one.
		it('wraps past a button taken out of the tab order', async () => {
			await trapped(
				aDialog('<button>One</button><button>Two</button><button tabindex="-1">Roving</button>')
			);
			button('Two').focus();
			await userEvent.tab();
			expect(button('One')).toHaveFocus();
		});

		it('does nothing with no stops in the dialog', async () => {
			await trapped(aDialog('<p>Nothing here</p>'));
			await userEvent.tab();
			expect(document.activeElement).not.toBe(null);
		});
	});

	describe('Escape', () => {
		it('calls onescape wherever the focus is', async () => {
			const onescape = vi.fn();
			await trapped(aDialog('<button>One</button>'), { onescape });

			opener.focus();
			await userEvent.keyboard('{Escape}');

			expect(onescape).toHaveBeenCalledOnce();
		});

		it('keeps the key from anything under the dialog', async () => {
			const under = vi.fn();
			document.addEventListener('keydown', under);
			await trapped(aDialog('<button>One</button>'));

			await userEvent.keyboard('{Escape}');
			document.removeEventListener('keydown', under);

			expect(under).not.toHaveBeenCalled();
		});

		it('uses the latest options after an update', async () => {
			const first = vi.fn();
			const second = vi.fn();
			const action = await trapped(aDialog('<button>One</button>'), { onescape: first });

			action?.update?.({ onescape: second });
			await userEvent.keyboard('{Escape}');

			expect(first).not.toHaveBeenCalled();
			expect(second).toHaveBeenCalledOnce();
		});

		it('leaves other keys alone', async () => {
			const onescape = vi.fn();
			await trapped(aDialog('<button>One</button>'), { onescape });
			await userEvent.keyboard('a{Enter}');
			expect(onescape).not.toHaveBeenCalled();
		});

		// A Select in a Modal (NewRunDialog, CopyFlow) must not lose the whole dialog
		// to the Esc meant for its list.
		it('lets an open list inside the dialog take Esc first', async () => {
			const onescape = vi.fn();
			const node = aDialog('');
			render(Select, {
				target: node,
				props: { value: 'a', options: [{ value: 'a', label: 'Alpha' }], label: 'Letter' }
			});
			await trapped(node, { onescape });
			await userEvent.click(screen.getByRole('combobox', { name: 'Letter' }));
			expect(screen.getByRole('listbox')).toBeInTheDocument();

			await userEvent.keyboard('{Escape}');

			await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull());
			expect(onescape).not.toHaveBeenCalled();
			expect(screen.getByRole('combobox', { name: 'Letter' })).toHaveFocus();

			await userEvent.keyboard('{Escape}');
			expect(onescape).toHaveBeenCalledOnce();
		});

		it('still keeps the key from anything under the dialog while a list takes it', async () => {
			const under = vi.fn();
			document.addEventListener('keydown', under);
			const node = aDialog('');
			render(Select, {
				target: node,
				props: { value: 'a', options: [{ value: 'a', label: 'Alpha' }], label: 'Letter' }
			});
			await trapped(node);
			await userEvent.click(screen.getByRole('combobox', { name: 'Letter' }));

			await userEvent.keyboard('{Escape}');
			document.removeEventListener('keydown', under);

			expect(under).not.toHaveBeenCalled();
		});

		it('closes only the top dialog of two, then the one under it', async () => {
			const under = vi.fn();
			const over = vi.fn();
			await trapped(aDialog('<button>One</button>'), { onescape: under });
			const top = await trapped(aDialog('<button>Two</button>'), { onescape: over });

			await userEvent.keyboard('{Escape}');
			expect(over).toHaveBeenCalledOnce();
			expect(under).not.toHaveBeenCalled();

			top.destroy?.();
			await userEvent.keyboard('{Escape}');
			expect(under).toHaveBeenCalledOnce();
		});
	});

	describe('leaving', () => {
		it('gives the focus back to where it was', async () => {
			const action = await trapped(aDialog('<button>One</button>'));
			expect(button('One')).toHaveFocus();

			action?.destroy?.();

			expect(opener).toHaveFocus();
		});

		it('stops listening', async () => {
			const onescape = vi.fn();
			const action = await trapped(aDialog('<button>One</button>'), { onescape });

			action?.destroy?.();
			await userEvent.keyboard('{Escape}');

			expect(onescape).not.toHaveBeenCalled();
		});

		it('copes with the opener having gone', async () => {
			const action = await trapped(aDialog('<button>One</button>'));
			opener.remove();
			expect(() => action?.destroy?.()).not.toThrow();
		});
	});
});
