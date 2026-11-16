import { fireEvent, render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { I } from '$lib/design/icons';
import { toasts } from './toast.svelte';
import RefChip from './RefChip.svelte';

const button = () => screen.getByRole('button', { name: 'Copy reference review#5' });

describe('RefChip', () => {
	beforeEach(() => (toasts.list = []));

	it('shows the reference and names what a click copies', () => {
		render(RefChip, { ref: 'review#5' });
		expect(button()).toHaveTextContent('review#5');
		expect(button()).toHaveAttribute('title', 'Copy reference review#5');
	});

	it('copies the reference, ticks, then goes back', async () => {
		const user = userEvent.setup();
		const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
		render(RefChip, { ref: 'review#5' });

		await user.click(button());

		expect(write).toHaveBeenCalledExactlyOnceWith('review#5');
		expect(button()).toHaveTextContent(`review#5${I.ok}`);
		await vi.waitFor(() => expect(button()).toHaveTextContent(/^review#5$/), { timeout: 3000 });
	});

	it('says in a toast when the browser refuses the clipboard', async () => {
		const user = userEvent.setup();
		vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'));
		render(RefChip, { ref: 'review#5' });

		await user.click(button());

		expect(toasts.list.map((x) => [x.text, x.tone])).toEqual([
			['Could not copy — the browser refused clipboard access.', 'bad']
		]);
		expect(button()).not.toHaveTextContent(I.ok);
	});

	it('copies without opening the row it sits in', async () => {
		vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
		const onrow = vi.fn();
		const host = document.createElement('a');
		host.href = '#row';
		host.addEventListener('click', onrow);
		const cell = host.appendChild(document.createElement('span'));
		document.body.append(host);
		render(RefChip, { target: cell, props: { ref: 'review#5' } });

		const fired = await fireEvent.click(button());

		expect(onrow).not.toHaveBeenCalled();
		// The default is prevented too: a chip inside a link does not follow it.
		expect(fired).toBe(false);
		host.remove();
	});

	it('is a plain chip inside something that is already a button', () => {
		render(RefChip, { ref: 'build#3/decision#1', plain: true });
		expect(screen.queryByRole('button')).not.toBeInTheDocument();
		expect(screen.getByText('build#3/decision#1')).toHaveAttribute(
			'title',
			'Reference build#3/decision#1'
		);
	});
});
