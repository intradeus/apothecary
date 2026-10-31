import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { confirmDialog, confirmState, type ConfirmOptions } from './confirm.svelte';
import ConfirmHost from './ConfirmHost.svelte';

const options = { title: 'Stop this run?', body: 'It <b>stops</b>.', action: 'Stop run' };
const gone = () => waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
const action = () => screen.getByRole('button', { name: 'Stop run' });
const cancel = () => screen.getByRole('button', { name: 'Cancel' });

/** The host, a question on it, and the promise of its answer. */
async function ask(over: Partial<ConfirmOptions> = {}) {
	render(ConfirmHost);
	const answer = confirmDialog({ ...options, ...over });
	await screen.findByRole('dialog', { name: over.title ?? options.title });
	return { answer };
}

describe('ConfirmHost', () => {
	afterEach(() => confirmState.settle(false));

	it('shows nothing until something asks', () => {
		render(ConfirmHost);
		expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
	});

	it('shows the question, with the markup of its body', async () => {
		await ask();
		expect(screen.getByText('stops').tagName).toBe('B');
		expect(action()).toBeInTheDocument();
		expect(cancel()).toBeInTheDocument();
	});

	it('answers yes from the action and closes', async () => {
		const { answer } = await ask();
		await userEvent.click(action());
		await expect(answer).resolves.toBe(true);
		await gone();
	});

	it('answers no from Cancel and closes', async () => {
		const { answer } = await ask();
		await userEvent.click(cancel());
		await expect(answer).resolves.toBe(false);
		await gone();
	});

	it('answers no on Escape', async () => {
		const { answer } = await ask();
		await userEvent.keyboard('{Escape}');
		await expect(answer).resolves.toBe(false);
		await gone();
	});

	it('answers no on a press on the scrim', async () => {
		const { answer } = await ask();
		await fireEvent.pointerDown(screen.getByRole('dialog').parentElement!);
		await expect(answer).resolves.toBe(false);
		await gone();
	});

	// A keyboard user confirms with Enter; Cancel is a Tab away.
	it('rests focus on the action, so Enter confirms', async () => {
		const { answer } = await ask();
		await waitFor(() => expect(action()).toHaveFocus());
		await userEvent.keyboard('{Enter}');
		await expect(answer).resolves.toBe(true);
	});

	it('takes another cancel label', async () => {
		await ask({ cancel: 'Keep it' });
		expect(screen.getByRole('button', { name: 'Keep it' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Cancel' })).not.toBeInTheDocument();
	});

	it.each([
		[undefined, '■', 'danger'],
		['danger', '■', 'danger'],
		['warn', '!', 'warn']
	] as const)('marks tone %s with %s', async (tone, glyph, cls) => {
		await ask({ tone });
		expect(screen.getByText(glyph)).toHaveClass(cls);
	});

	it('answers no to a question a new one replaces', async () => {
		const { answer } = await ask();

		const next = confirmDialog({ ...options, title: 'Delete it?', action: 'Delete' });

		await expect(answer).resolves.toBe(false);
		await screen.findByRole('dialog', { name: 'Delete it?' });
		await userEvent.click(screen.getByRole('button', { name: 'Delete' }));
		await expect(next).resolves.toBe(true);
	});

	it('asks again after the last one closed', async () => {
		const first = await ask();
		await userEvent.click(cancel());
		await first.answer;
		await gone();

		const again = confirmDialog({ ...options, title: 'Really?' });
		await screen.findByRole('dialog', { name: 'Really?' });
		await userEvent.click(action());

		await expect(again).resolves.toBe(true);
	});
});
