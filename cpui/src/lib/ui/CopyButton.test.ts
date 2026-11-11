import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { I } from '$lib/design/icons';
import { toasts } from './toast.svelte';
import CopyButton from './CopyButton.svelte';

const CMD = 'apothecary forward apo-main-abcd 3000';
const button = () => screen.getByRole('button', { name: `Copy ${CMD}` });

describe('CopyButton', () => {
	beforeEach(() => (toasts.list = []));
	afterEach(() => vi.useRealTimers());

	it('names the command it copies, in its label and its tooltip', () => {
		render(CopyButton, { command: CMD });
		expect(button()).toHaveAttribute('title', CMD);
		expect(button()).toHaveTextContent(I.copy);
		expect(screen.queryByText(CMD)).not.toBeInTheDocument();
	});

	it('copies the command, ticks, then goes back', async () => {
		const user = userEvent.setup();
		const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
		render(CopyButton, { command: CMD });

		await user.click(button());

		expect(write).toHaveBeenCalledExactlyOnceWith(CMD);
		expect(button()).toHaveTextContent(I.ok);
		expect(button()).toHaveClass('hue-success');
		await vi.waitFor(() => expect(button()).toHaveTextContent(I.copy), { timeout: 3000 });
	});

	it('says in a toast when the browser refuses the clipboard', async () => {
		const user = userEvent.setup();
		vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'));
		render(CopyButton, { command: CMD });

		await user.click(button());

		expect(toasts.list.map((x) => [x.text, x.tone])).toEqual([
			['Could not copy — the browser refused clipboard access.', 'bad']
		]);
		expect(button()).toHaveTextContent(I.copy);
	});
});
