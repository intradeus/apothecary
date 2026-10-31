import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { bound, html, nested } from '$test/fixtures/designsystem.svelte';
import { text } from '$test/helpers';
import Modal from './Modal.svelte';
import Select from './Select.svelte';

const dialog = () => screen.getByRole('dialog');
const gone = () => waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
const setup = (rest: Record<string, unknown> = {}) => {
	const onclose = vi.fn();
	const b = bound('open', true, {
		title: 'Stop this run?',
		children: text('The body'),
		onclose,
		...rest
	});
	render(Modal, b.props);
	return { ...b, onclose };
};
const fields = html('<form><input aria-label="Name" /><input aria-label="Other" /></form>');
const buttons = html('<div><button>Cancel</button><button>Stop</button></div>');

describe('Modal', () => {
	it('renders nothing while closed', () => {
		const b = bound('open', false, { title: 'Hidden', children: text('The body') });
		render(Modal, b.props);
		expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
	});

	it('is a modal dialog named by its title', () => {
		setup();
		expect(screen.getByRole('dialog', { name: 'Stop this run?' })).toHaveAttribute(
			'aria-modal',
			'true'
		);
		expect(screen.getByRole('heading', { name: 'Stop this run?' })).toBeInTheDocument();
		expect(within(dialog()).getByText('The body')).toBeInTheDocument();
	});

	it('lives under <body>, out of any pane that would clip it', () => {
		const { container } = render(Modal, { open: true, title: 'x', children: text('y') });
		expect(container).not.toContainElement(dialog());
		expect(dialog().parentElement!.parentElement).toBe(document.body);
	});

	it('renders the footer it is given', () => {
		setup({ footer: buttons });
		expect(within(dialog()).getByRole('button', { name: 'Stop' })).toBeInTheDocument();
	});

	it('draws the icon in its tone', () => {
		setup({ icon: '■', tone: 'danger' });
		expect(screen.getByText('■')).toHaveClass('danger');
	});

	it('has no icon unless given one', () => {
		setup();
		expect(dialog().querySelector('.ic')).not.toBeInTheDocument();
	});

	describe('focus', () => {
		it('moves to the first field', async () => {
			setup({ children: fields, footer: buttons });
			await waitFor(() => expect(screen.getByLabelText('Name')).toHaveFocus());
		});

		// The action, not Cancel.
		it('moves to the last button when there are no fields', async () => {
			setup({ footer: buttons });
			await waitFor(() => expect(screen.getByRole('button', { name: 'Stop' })).toHaveFocus());
		});

		it('cycles Tab forwards inside the dialog', async () => {
			setup({ children: fields, footer: buttons });
			await waitFor(() => expect(screen.getByLabelText('Name')).toHaveFocus());

			await userEvent.tab();
			expect(screen.getByLabelText('Other')).toHaveFocus();
			await userEvent.tab();
			expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus();
			await userEvent.tab();
			expect(screen.getByRole('button', { name: 'Stop' })).toHaveFocus();
			await userEvent.tab();
			expect(screen.getByLabelText('Name')).toHaveFocus();
		});

		it('cycles Shift+Tab backwards from the first field', async () => {
			setup({ children: fields, footer: buttons });
			await waitFor(() => expect(screen.getByLabelText('Name')).toHaveFocus());

			await userEvent.tab({ shift: true });
			expect(screen.getByRole('button', { name: 'Stop' })).toHaveFocus();
		});

		describe('with an opener', () => {
			const opener = document.createElement('button');
			afterEach(() => opener.remove());

			it('returns to the opener on close', async () => {
				document.body.append(opener);
				opener.focus();
				setup({ footer: buttons });
				await waitFor(() => expect(screen.getByRole('button', { name: 'Stop' })).toHaveFocus());

				await userEvent.keyboard('{Escape}');
				await gone();

				expect(opener).toHaveFocus();
			});
		});
	});

	describe('closing', () => {
		it('closes on Escape and says so once', async () => {
			const { current, onclose } = setup();

			await userEvent.keyboard('{Escape}');

			expect(current()).toBe(false);
			expect(onclose).toHaveBeenCalledOnce();
			await gone();
		});

		it('closes on a press on the scrim', async () => {
			const { current, onclose } = setup();

			await fireEvent.pointerDown(dialog().parentElement!);

			expect(current()).toBe(false);
			expect(onclose).toHaveBeenCalledOnce();
			await gone();
		});

		it('stays open for a press inside the dialog', async () => {
			const { current, onclose } = setup();

			await fireEvent.pointerDown(within(dialog()).getByText('The body'));

			expect(current()).toBe(true);
			expect(onclose).not.toHaveBeenCalled();
		});

		// ConfirmHost relies on this: it settled before it closed.
		it('does not call onclose when the parent closes it', async () => {
			const { props, onclose } = setup();

			props.open = false;
			await tick();
			await gone();

			expect(onclose).not.toHaveBeenCalled();
		});

		// NewRunDialog, CopyFlow and AutomationsTab each hold a Select.
		it('leaves the dialog open when Escape closes a list inside it', async () => {
			const { current } = setup({
				children: nested(Select, {
					label: 'Flow',
					value: 'a',
					options: [
						{ value: 'a', label: 'Alpha' },
						{ value: 'b', label: 'Beta' }
					]
				})
			});
			await userEvent.click(screen.getByRole('combobox', { name: 'Flow' }));
			expect(screen.getByRole('listbox')).toBeInTheDocument();

			await userEvent.keyboard('{Escape}');

			expect(current()).toBe(true);
			await waitFor(() => expect(screen.queryByRole('listbox')).not.toBeInTheDocument());
		});
	});

	it('does not fire a stale listener after closing', async () => {
		const { onclose } = setup();
		await userEvent.keyboard('{Escape}');
		await gone();

		await userEvent.keyboard('{Escape}');

		expect(onclose).toHaveBeenCalledOnce();
	});
});
