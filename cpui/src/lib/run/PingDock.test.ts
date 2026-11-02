import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { aRun } from '$test/fixtures';
import { aRaise } from '$test/fixtures/run-views';
import { pingsOf } from './derive';
import PingDock from './PingDock.svelte';

vi.mock('$lib/api', () => ({ api: { files: { limits: vi.fn(async () => null) } } }));

const ping = (id: string, question = `Question ${id}?`) =>
	pingsOf([aRaise(1, id, { question })], aRun())[0];

const props = (over = {}) => ({
	ping: ping('p1'),
	runId: 'r1',
	queued: 0,
	busy: false,
	onanswer: vi.fn(),
	...over
});

describe('PingDock', () => {
	it('shows the ping and hands its answer up', async () => {
		const onanswer = vi.fn();
		render(PingDock, props({ onanswer }));
		expect(screen.getByText('Question p1?')).toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Approve' }));
		expect(onanswer).toHaveBeenCalledWith('approve', '', undefined, undefined);
	});

	it('says nothing of a queue when nothing waits', () => {
		render(PingDock, props());
		expect(screen.queryByText(/answer this first/i)).not.toBeInTheDocument();
	});

	it.each([
		[1, 'Answer this first — 1 earlier ping queued'],
		[3, 'Answer this first — 3 earlier pings queued']
	])('says when %i wait behind it', (queued, text) => {
		render(PingDock, props({ queued }));
		expect(screen.getByText(text)).toBeInTheDocument();
	});

	it('holds the card while busy', () => {
		render(PingDock, props({ busy: true }));
		expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
	});

	// An answer reveals the next ping in the same dock: a half-written reason
	// must not carry over and be sent as the answer to a different question.
	it('starts the next ping with a clean card', async () => {
		const { rerender } = render(PingDock, props());
		await userEvent.click(screen.getByRole('button', { name: 'Request changes…' }));
		await userEvent.type(screen.getByRole('textbox'), 'About the first one');

		await rerender({ ping: ping('p2') });

		expect(screen.getByText('Question p2?')).toBeInTheDocument();
		expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole('button', { name: 'Request changes…' }));
		expect(screen.getByRole('textbox')).toHaveValue('');
	});
});
