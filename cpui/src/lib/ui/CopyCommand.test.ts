import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import CopyCommand from './CopyCommand.svelte';

const CMD = 'apothecary forward 01RUN';
const button = () => screen.getByRole('button', { name: `Copy ${CMD}` });

describe('CopyCommand', () => {
	afterEach(() => vi.useRealTimers());

	it('shows the command and what it does', () => {
		render(CopyCommand, { command: CMD, help: 'Every port, on your machine' });
		expect(screen.getByText(CMD)).toHaveAttribute('title', CMD);
		expect(screen.getByText('Every port, on your machine')).toBeInTheDocument();
	});

	it('copies the command, says so, then goes back', async () => {
		const user = userEvent.setup();
		const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
		render(CopyCommand, { command: CMD });
		expect(button()).toHaveTextContent('copy');

		await user.click(button());

		expect(write).toHaveBeenCalledExactlyOnceWith(CMD);
		expect(button()).toHaveTextContent('copied');
		await vi.waitFor(() => expect(button()).toHaveTextContent(/^copy$/), { timeout: 3000 });
	});

	it('says when the browser refuses the clipboard, in place of the help', async () => {
		const user = userEvent.setup();
		vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'));
		render(CopyCommand, { command: CMD, help: 'Every port' });

		await user.click(button());

		expect(await screen.findByText(/refused clipboard access/)).toBeInTheDocument();
		expect(screen.queryByText('Every port')).not.toBeInTheDocument();
		expect(button()).toHaveTextContent('copy');
	});
});
