import { render, screen, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import { fmtTime } from '$lib/core/format';
import type { FeedEntry } from './feed';
import KillFeed from './KillFeed.svelte';

const AT = '2026-10-31T12:00:05Z';
const nameOf = (id: string) => ({ p1: 'Billing', p2: 'Docs' })[id] ?? id;

const line = (over: Partial<FeedEntry>): FeedEntry => ({
	id: `${over.kind}-${over.runId}`,
	at: AT,
	kind: 'deployed',
	runId: 'r1',
	projectId: 'p1',
	title: 'Add idempotency keys',
	...over
});

const items = () =>
	within(screen.getByRole('list', { name: 'Kill feed' })).getAllByRole('listitem');

describe('KillFeed', () => {
	it('is a polite live list', () => {
		render(KillFeed, { entries: [], nameOf });
		expect(screen.getByRole('list', { name: 'Kill feed' })).toHaveAttribute('aria-live', 'polite');
	});

	it('stands by when nothing has happened', () => {
		render(KillFeed, { entries: [], nameOf });
		expect(items()).toHaveLength(1);
		expect(screen.getByText('Standing by…')).toBeInTheDocument();
	});

	it('keeps the order it is given: newest first', () => {
		render(KillFeed, {
			entries: [
				line({ kind: 'failed', runId: 'r2', title: 'Newest' }),
				line({ kind: 'deployed', runId: 'r1', title: 'Oldest' })
			],
			nameOf
		});
		const [first, second] = items();
		expect(first).toHaveTextContent('Newest');
		expect(second).toHaveTextContent('Oldest');
	});

	it('says when, where, who and what', () => {
		render(KillFeed, { entries: [line({ kind: 'deployed', projectId: 'p2' })], nameOf });
		const [only] = items();
		expect(within(only).getByText(fmtTime(AT))).toBeInTheDocument();
		expect(within(only).getByText('Docs')).toBeInTheDocument();
		expect(within(only).getByText('Add idempotency keys')).toBeInTheDocument();
		expect(within(only).getByText('deployed')).toBeInTheDocument();
	});

	it.each([
		[{ kind: 'stage', detail: 'plan' }, 'cleared stage plan', 'tone-running'],
		[{ kind: 'drone', detail: 'scout the API' }, 'launched drone: scout the API', 'tone-accent'],
		[{ kind: 'needs_you' }, 'needs you', 'tone-attention'],
		[{ kind: 'in_review' }, 'in review', 'tone-accent-2'],
		[{ kind: 'completed' }, 'completed', 'tone-success'],
		[{ kind: 'failed' }, 'failed', 'tone-danger'],
		[{ kind: 'abandoned' }, 'abandoned', 'tone-warning']
	] as [Partial<FeedEntry>, string, string][])('phrases %o as "%s"', (over, text, tone) => {
		render(KillFeed, { entries: [line(over)], nameOf });
		expect(screen.getByText(text)).toBeInTheDocument();
		expect(items()[0]).toHaveClass(tone);
	});
});
