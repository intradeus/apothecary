import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { fleet } from '$lib/app/fleet.svelte';
import { fmtClock } from '$lib/core/format';
import type { RunEvent } from '$lib/domain';
import { to } from '$lib/core/urls';
import { aRun } from '$test/fixtures';
import { NOW, aRaise, anAnswer, aViewStore } from '$test/fixtures/run-views';
import { deferred } from '$test/helpers';
import PingsView from './PingsView.svelte';

vi.mock('$lib/api', () => ({
	api: {
		pings: { answer: vi.fn(), payload: vi.fn(), curation: vi.fn() },
		files: { upload: vi.fn(), snapshotUrl: () => '', attachmentUrl: () => '' },
		runs: { files: vi.fn() }
	}
}));

const row = (question: RegExp) => screen.getByRole('button', { name: question });

function show(events: RunEvent[], run = aRun()) {
	const rs = aViewStore(run, { events });
	render(PingsView, { rs });
	return rs;
}

describe('PingsView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.setSystemTime(NOW + 180_000);
		vi.spyOn(fleet, 'refresh').mockResolvedValue();
	});

	describe('the list', () => {
		it('says when the run has raised none', () => {
			show([]);
			expect(screen.getByText('none yet')).toBeInTheDocument();
			expect(screen.getByText(/^No pings\./)).toBeInTheDocument();
		});

		it('sums up what is pending, answered and withdrawn', () => {
			show([aRaise(1, 'a'), aRaise(2, 'b'), aRaise(3, 'c'), anAnswer(4, 'a')]);
			expect(screen.getByText('2 pending · 1 answered')).toBeInTheDocument();
		});

		it('counts a finished run’s open pings as withdrawn', () => {
			show([aRaise(1, 'a'), aRaise(2, 'b'), anAnswer(3, 'a')], aRun({ state: 'abandoned' }));
			expect(screen.getByText('1 answered · 1 withdrawn')).toBeInTheDocument();
		});

		it('leads with the newest', () => {
			show([aRaise(1, 'old'), anAnswer(2, 'old'), aRaise(3, 'new'), anAnswer(4, 'new')]);
			const questions = screen.getAllByText(/^Question /).map((e) => e.textContent);
			expect(questions).toEqual(['Question new?', 'Question old?']);
		});
	});

	describe('a pending ping', () => {
		it('opens as the card that answers it', async () => {
			vi.mocked(api.pings.answer).mockResolvedValue(undefined as never);
			show([aRaise(1, 'a')]);

			await userEvent.click(screen.getByRole('button', { name: 'Approve' }));

			expect(api.pings.answer).toHaveBeenCalledExactlyOnceWith(
				'a',
				expect.objectContaining({ answer: 'approve' })
			);
		});

		it('sends the feedback that rides on a rejection', async () => {
			vi.mocked(api.pings.answer).mockResolvedValue(undefined as never);
			show([aRaise(1, 'a')]);

			await userEvent.click(screen.getByRole('button', { name: 'Request changes…' }));
			await userEvent.type(screen.getByRole('textbox'), 'Smaller, please');
			await userEvent.click(screen.getByRole('button', { name: 'Send changes' }));

			expect(api.pings.answer).toHaveBeenCalledExactlyOnceWith(
				'a',
				expect.objectContaining({ answer: 'reject', feedback: 'Smaller, please' })
			);
		});

		it('holds the card while the answer is on its way', async () => {
			const sent = deferred();
			vi.mocked(api.pings.answer).mockReturnValue(sent.promise as never);
			show([aRaise(1, 'a')]);

			await userEvent.click(screen.getByRole('button', { name: 'Approve' }));

			expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
			sent.resolve();
		});

		it('leaves a failed answer on the store, for the header to show', async () => {
			vi.mocked(api.pings.answer).mockRejectedValue(new Error('already answered'));
			const rs = show([aRaise(1, 'a')]);

			await userEvent.click(screen.getByRole('button', { name: 'Approve' }));

			await vi.waitFor(() => expect(rs.error).toBe('already answered'));
		});
	});

	describe('a settled ping', () => {
		it('sums itself up in one row', () => {
			show([aRaise(1, 'a', { stage_id: 'plan' }), anAnswer(2, 'a')]);

			const r = row(/Question a/);
			expect(r).toHaveTextContent('Gate');
			expect(r).toHaveTextContent(`plan · ${fmtClock(new Date(NOW + 1000))}`);
			expect(r).toHaveTextContent('wanted');
			expect(r).toHaveTextContent('approve · 2m ago');
		});

		it('marks an agent’s own question as unwanted', () => {
			show([
				aRaise(1, 'a', { type: 'clarification', answer_kind: 'form' }),
				anAnswer(2, 'a', { answer: 'accept' })
			]);
			expect(row(/Question a/)).toHaveTextContent('Clarification');
			expect(row(/Question a/)).toHaveTextContent('unwanted');
		});

		it.each([
			['accept', 'Answered'],
			['decline', 'Declined']
		])('words a form answered %s', (answer, word) => {
			show([aRaise(1, 'a', { answer_kind: 'form' }), anAnswer(2, 'a', { answer })]);
			expect(row(/Question a/)).toHaveTextContent(word);
		});

		it('strikes a withdrawn question and says so', () => {
			show([aRaise(1, 'a')], aRun({ state: 'failed' }));
			expect(screen.getByText('Question a?')).toHaveClass('struck');
			expect(row(/Question a/)).toHaveTextContent('withdrawn');
		});

		it('offers no card to answer on a finished run', () => {
			show([aRaise(1, 'a')], aRun({ state: 'failed' }));
			expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument();
		});

		it('opens to the question, the feedback and the way to the inbox', async () => {
			show([
				aRaise(1, 'a', { question: 'Is **this** fine?' }),
				anAnswer(2, 'a', { feedback: 'Mostly' })
			]);

			await userEvent.click(row(/Is \*\*this\*\* fine/));

			expect(row(/Is \*\*this\*\* fine/)).toHaveAttribute('aria-expanded', 'true');
			expect(screen.getByText('this').tagName).toBe('STRONG');
			expect(screen.getByText('You:').parentElement).toHaveTextContent('You: Mostly');
			expect(screen.getByRole('link', { name: /Open in inbox/ })).toHaveAttribute(
				'href',
				to.ping('a')
			);
		});

		it('closes again on a second click', async () => {
			show([aRaise(1, 'a'), anAnswer(2, 'a')]);
			await userEvent.click(row(/Question a/));
			await userEvent.click(row(/Question a/));

			expect(row(/Question a/)).toHaveAttribute('aria-expanded', 'false');
			expect(screen.queryByRole('link')).not.toBeInTheDocument();
		});

		it('keeps one open at a time', async () => {
			show([aRaise(1, 'a'), anAnswer(2, 'a'), aRaise(3, 'b'), anAnswer(4, 'b')]);
			await userEvent.click(row(/Question a/));
			await userEvent.click(row(/Question b/));

			expect(row(/Question a/)).toHaveAttribute('aria-expanded', 'false');
			expect(row(/Question b/)).toHaveAttribute('aria-expanded', 'true');
			expect(screen.getAllByRole('link')).toHaveLength(1);
		});

		it('shows no "You:" when no feedback came with the answer', async () => {
			show([aRaise(1, 'a'), anAnswer(2, 'a')]);
			await userEvent.click(row(/Question a/));
			expect(screen.queryByText('You:')).not.toBeInTheDocument();
		});

		it('has no answer to show on a withdrawn ping', async () => {
			show([aRaise(1, 'a', { files: ['plan.md'] })], aRun({ state: 'failed' }));
			await userEvent.click(row(/Question a/));

			expect(api.pings.payload).not.toHaveBeenCalled();
			expect(screen.getByRole('link', { name: /Open in inbox/ })).toBeInTheDocument();
		});
	});

	describe('what a ping showed', () => {
		it('opens a document ping on its review as submitted', async () => {
			vi.mocked(api.pings.payload).mockResolvedValue({ files: [] } as never);
			show([aRaise(1, 'a', { files: ['plan.md'] }), anAnswer(2, 'a', { feedback: 'Looks right' })]);

			await userEvent.click(row(/Question a/));

			expect(api.pings.payload).toHaveBeenCalledExactlyOnceWith('a');
			expect(api.pings.curation).not.toHaveBeenCalled();
			// The summary, not the plain "You:" line.
			expect(await screen.findByText('Looks right')).toBeInTheDocument();
			expect(screen.queryByText('You:')).not.toBeInTheDocument();
		});

		it('reads attachments from the run, not from the network again', async () => {
			vi.mocked(api.pings.payload).mockResolvedValue({ files: [] } as never);
			show([
				aRaise(1, 'a', { files: ['plan.md'] }),
				anAnswer(2, 'a', { attachment_ids: ['att-1'] })
			]);

			await userEvent.click(row(/Question a/));

			expect(api.runs.files).not.toHaveBeenCalled();
		});

		it('opens a curated ping on the curation, with its feedback', async () => {
			vi.mocked(api.pings.curation).mockResolvedValue({ documents: [] } as never);
			show([
				aRaise(1, 'a', {
					files: ['review.yaml'],
					file_types: { 'review.yaml': 'review_threads' }
				}),
				anAnswer(2, 'a', { feedback: 'Fine by me' })
			]);

			await userEvent.click(row(/Question a/));

			expect(api.pings.curation).toHaveBeenCalledExactlyOnceWith('a');
			expect(api.pings.payload).not.toHaveBeenCalled();
			expect(screen.getByText('You:').parentElement).toHaveTextContent('You: Fine by me');
			expect(await screen.findByRole('alert')).toHaveTextContent(
				'Could not read what the gate showed'
			);
		});

		it('keeps the row usable when the curation cannot be read', async () => {
			vi.mocked(api.pings.curation).mockRejectedValue(new Error('gone'));
			show([
				aRaise(1, 'a', {
					files: ['review.yaml'],
					file_types: { 'review.yaml': 'review_replies' }
				}),
				anAnswer(2, 'a')
			]);

			await userEvent.click(row(/Question a/));

			expect(await screen.findByRole('alert')).toBeInTheDocument();
			expect(within(screen.getByRole('alert')).getByText('gone')).toBeInTheDocument();
		});
	});
});
