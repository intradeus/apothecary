import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { createRawSnippet } from 'svelte';
import { describe, expect, it } from 'vitest';
import InfoTip from './InfoTip.svelte';

const body = createRawSnippet(() => ({ render: () => '<span>Sent SIGTERM, then SIGKILL.</span>' }));

function open() {
	const view = render(InfoTip, { label: 'What stop does', children: body });
	const button = screen.getByRole('button', { name: 'What stop does' });
	const tip = screen.getByRole('tooltip', { hidden: true });
	return { ...view, button, tip };
}

describe('InfoTip', () => {
	it('describes its button even while hidden', () => {
		const { button, tip } = open();
		expect(tip).not.toBeVisible();
		expect(button).toHaveAccessibleDescription('Sent SIGTERM, then SIGKILL.');
		expect(button).toHaveAttribute('aria-expanded', 'false');
	});

	it('lives under <body>, out of any pane that would clip it', () => {
		const { container, tip } = open();
		expect(tip.parentElement).toBe(document.body);
		expect(container.contains(tip)).toBe(false);
	});

	it('shows on hover above the button, and hides on leave', async () => {
		const { button, tip } = open();
		button.getBoundingClientRect = () => ({ left: 100, top: 40, width: 16, height: 16 }) as DOMRect;

		await fireEvent.mouseEnter(button);
		expect(tip).toBeVisible();
		expect(tip.style.left).toBe('108px');
		expect(tip.style.top).toBe('40px');
		expect(button).toHaveAttribute('aria-expanded', 'true');

		await fireEvent.mouseLeave(button);
		expect(tip).not.toBeVisible();
	});

	it('shows on focus, and hides on Esc or blur', async () => {
		const { button, tip } = open();
		await userEvent.tab();
		expect(button).toHaveFocus();
		expect(tip).toBeVisible();

		await userEvent.keyboard('{Escape}');
		expect(tip).not.toBeVisible();
		// Esc with nothing open is left to whatever holds the field.
		await userEvent.keyboard('{Escape}');

		await fireEvent.focus(button);
		expect(tip).toBeVisible();
		await fireEvent.blur(button);
		expect(tip).not.toBeVisible();
	});

	it('toggles on a tap, for screens with no hover', async () => {
		const { button, tip } = open();
		await fireEvent.click(button);
		expect(tip).toBeVisible();
		await fireEvent.click(button);
		expect(tip).not.toBeVisible();
	});

	it('takes itself away with its owner', () => {
		const { unmount, tip } = open();
		unmount();
		expect(document.body.contains(tip)).toBe(false);
	});
});
