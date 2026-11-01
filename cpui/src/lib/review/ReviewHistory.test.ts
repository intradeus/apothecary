import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import type { ComponentProps } from 'svelte';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import {
	aPingFile,
	aReviewComment,
	anAttachment,
	serveSnapshots,
	stubGeometry
} from '$test/fixtures/review';
import ReviewHistory from './ReviewHistory.svelte';
import { prefs } from '$lib/app/prefs.svelte';

vi.mock('$lib/api', () => ({
	api: {
		pings: { payload: vi.fn() },
		runs: { files: vi.fn() },
		files: {
			snapshotUrl: (id: string) => `/api/snapshots/${id}`,
			attachmentUrl: (id: string) => `/api/attachments/${id}`
		}
	}
}));

const PLAN = '# Plan\n\nShip the webhook first.\n\n## Risks\n\nRetries may double charge.\n';
const SHIP = 'Ship the webhook first.';
const plan = aPingFile({ path: 'plan.md', snapshotId: 'plan' });
const cfg = aPingFile({ path: 'cfg.yaml', snapshotId: 'cfg', mime: 'text/yaml' });
const base = { pingId: 'p1', runId: 'r1', raisedAt: '2026-10-31T12:00:00Z' };
const onCfg = aReviewComment({ path: 'cfg.yaml', body: 'Too many retries' });
const onRisks = aReviewComment({
	startLine: 7,
	endLine: 7,
	heading: 'Risks',
	quote: 'double charge',
	body: 'How likely?'
});

async function mount(over: Partial<ComponentProps<typeof ReviewHistory>> = {}) {
	vi.mocked(api.pings.payload).mockResolvedValue({ files: [plan, cfg] });
	serveSnapshots({ plan: PLAN, cfg: 'retries: 9\n' });
	const view = render(ReviewHistory, { ...base, ...over });
	await screen.findByText(SHIP);
	return view;
}

const allFiles = () => screen.getByRole('radio', { name: /All files/ });

describe('ReviewHistory', () => {
	beforeAll(stubGeometry);
	beforeEach(() => {
		vi.resetAllMocks();
		prefs.setFileLayout('list');
	});

	it('shows skeletons until the files are known', () => {
		vi.mocked(api.pings.payload).mockReturnValue(new Promise(() => {}));
		render(ReviewHistory, base);
		expect(screen.getAllByLabelText('Loading')).toHaveLength(2);
	});

	it('says why the files could not be listed', async () => {
		vi.mocked(api.pings.payload).mockRejectedValue(new Error('snapshots pruned'));
		render(ReviewHistory, base);
		expect(await screen.findByRole('alert')).toHaveTextContent('snapshots pruned');
	});

	it('shows each file as it was reviewed', async () => {
		await mount();
		expect(screen.getByText(/as reviewed · snapshot from/)).toBeInTheDocument();
		expect(api.pings.payload).toHaveBeenCalledWith('p1');
	});

	it('is read-only: no composer, no answer, no edit', async () => {
		await mount({ review: [onRisks] });
		for (const name of [/Comment on this/, /^Approve/, 'Edit', 'Delete comment'])
			expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
	});

	it('counts the comments on the review and on their files', async () => {
		await mount({ review: [onRisks, onCfg] });
		expect(screen.getByText('Review').parentElement).toHaveTextContent(/^Review\s*2$/);
		const files = screen.getByRole('navigation', { name: 'Files under review' });
		expect(within(files).getByTitle('cfg.yaml')).toHaveTextContent('1');
	});

	it('lights a comment’s quote where it was made', async () => {
		await mount({ review: [onRisks] });
		await waitFor(() =>
			expect(screen.getByText('Retries may double charge.').closest('.block')).toHaveClass(
				'commented'
			)
		);
		expect(screen.getByText(SHIP).closest('.block')).not.toHaveClass('commented');
	});

	it('lists the comments in file order and by position', async () => {
		const early = aReviewComment({ startLine: 3, endLine: 3, body: 'Earlier in the file' });
		await mount({ review: [onCfg, onRisks, early] });
		await userEvent.click(allFiles());
		const cards = screen.getAllByRole('listitem');
		expect(cards[0]).toHaveTextContent('Earlier in the file');
		expect(cards[1]).toHaveTextContent('How likely?');
		expect(cards[2]).toHaveTextContent('Too many retries');
	});

	it('says no comments are on the open file when they are elsewhere', async () => {
		await mount({ review: [onCfg] });
		expect(screen.getByText('No comments on this file.')).toBeInTheDocument();
	});

	it('opens a comment’s file from the list', async () => {
		await mount({ review: [onCfg] });
		await userEvent.click(allFiles());
		await userEvent.click(screen.getByRole('button', { name: 'whole file' }));
		expect(await screen.findByText('retries: 9')).toBeInTheDocument();
	});

	it('lights a comment’s block while it is hovered', async () => {
		await mount({ review: [onRisks] });
		const block = screen.getByText('Retries may double charge.').closest('.block');

		await userEvent.hover(screen.getByRole('listitem'));
		expect(block).toHaveClass('active');

		await userEvent.unhover(screen.getByRole('listitem'));
		expect(block).not.toHaveClass('active');
	});

	it('shows the summary', async () => {
		await mount({ summary: 'Mostly **fine**' });
		expect(screen.getByText('fine')).toBeInTheDocument();
		expect(screen.queryByText('Answered without comments.')).not.toBeInTheDocument();
	});

	it('says when a review was answered with nothing said', async () => {
		await mount();
		expect(screen.getByText('Answered without comments.')).toBeInTheDocument();
	});

	it('outlines a markdown file', async () => {
		await mount();
		expect(screen.getByRole('navigation', { name: 'Document outline' })).toBeInTheDocument();
	});

	describe('moving with keys', () => {
		it('moves between files with j', async () => {
			await mount();
			await userEvent.keyboard('j');
			expect(await screen.findByText('retries: 9')).toBeInTheDocument();
		});

		it('leaves the keys to the page when told to', async () => {
			await mount({ keys: false });
			await userEvent.keyboard('j');
			expect(screen.getByRole('button', { name: /plan\.md/ })).toHaveAttribute(
				'aria-current',
				'true'
			);
		});
	});

	describe('attachments', () => {
		const held = [anAttachment({ id: 'a1', filename: 'whole.txt' })];

		it('uses the run’s attachments when the caller holds them', async () => {
			await mount({ attachmentIds: ['a1'], attachments: held });
			expect(screen.getByRole('link', { name: /whole\.txt/ })).toHaveAttribute(
				'href',
				'/api/attachments/a1'
			);
			expect(api.runs.files).not.toHaveBeenCalled();
		});

		it('fetches them otherwise', async () => {
			vi.mocked(api.runs.files).mockResolvedValue({ attachments: held } as never);
			await mount({ attachmentIds: ['a1'] });
			expect(await screen.findByRole('link', { name: /whole\.txt/ })).toBeInTheDocument();
			expect(api.runs.files).toHaveBeenCalledExactlyOnceWith('r1');
		});

		it('fetches nothing with nothing attached', async () => {
			await mount();
			expect(api.runs.files).not.toHaveBeenCalled();
		});

		it('names what a failed fetch could not find', async () => {
			vi.mocked(api.runs.files).mockRejectedValue(new Error('nope'));
			await mount({ attachmentIds: ['a1'] });
			expect(await screen.findByText('not found')).toBeInTheDocument();
			expect(screen.getByText('a1')).toBeInTheDocument();
		});

		it('shows on the comment what a comment names, on the review only the rest', async () => {
			const both = [...held, anAttachment({ id: 'a2', filename: 'comment.txt' })];
			const review = [aReviewComment({ startLine: 3, endLine: 3, attachmentIds: ['a2'] })];
			await mount({ review, attachmentIds: ['a1', 'a2'], attachments: both });

			const card = screen.getByRole('listitem');
			expect(within(card).getByRole('link', { name: /comment\.txt/ })).toBeInTheDocument();
			expect(within(card).queryByRole('link', { name: /whole\.txt/ })).not.toBeInTheDocument();
			expect(screen.getAllByRole('link', { name: /comment\.txt/ })).toHaveLength(1);
			expect(screen.getAllByRole('link', { name: /whole\.txt/ })).toHaveLength(1);
		});
	});
});
