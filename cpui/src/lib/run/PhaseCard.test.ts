import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { PhaseHold, RunPhase } from '$lib/domain';
import { aPhase } from '$test/fixtures/run-components';
import PhaseCard from './PhaseCard.svelte';

type Props = {
	titles: Record<string, string>;
	ended: boolean;
	free: boolean;
	spent: boolean;
	busy: boolean;
	locked: boolean;
	onrun: (ev: MouseEvent) => void;
};

function show(over: Partial<RunPhase> = {}, props: Partial<Props> = {}) {
	const phase = aPhase({ id: 'p', ...over });
	return render(PhaseCard, {
		phase,
		titles: {},
		ended: false,
		free: true,
		spent: false,
		busy: false,
		locked: false,
		onrun: vi.fn(),
		...props
	});
}
const card = () => screen.getByRole('article', { name: 'p' });
/** The fact under its label in the card's list. */
const fact = (label: string) =>
	within(card()).queryByText(label)?.nextElementSibling?.textContent ?? null;
const runNow = () => screen.getByRole('button', { name: 'Run now' });
const hold = (why: string, seen: boolean): PhaseHold => ({
	subject: 'repo',
	key: why,
	why,
	seen,
	observedAt: ''
});

describe('PhaseCard', () => {
	describe('what opens it', () => {
		it.each([
			[
				'the main chain',
				{ kind: 'main', signal: '' },
				{ opens: 'when the run starts', starts: null, runs: 'once' }
			],
			[
				'by hand',
				{ signal: 'manual', max: 2, total: 1 },
				{ opens: 'only when you start it', starts: null, runs: '1/2 runs' }
			],
			[
				'a watcher',
				{ signal: 'ci_failed', start: 'confirm', max: -1, total: 2 },
				{ opens: 'ci_failed', starts: 'after you confirm it in the inbox', runs: '2/∞ runs' }
			],
			[
				'a watcher with a quiet time',
				{ signal: 'review_comment', quietForS: 300, start: 'auto', total: 0, max: 3 },
				{ opens: 'review_comment, quiet for 5m', starts: 'by itself', runs: '0/3 runs' }
			],
			[
				'an outcome',
				{ kind: 'outcome', signal: 'mr_merged', then: 'done', start: 'auto' },
				{ opens: 'mr_merged', starts: 'by itself', runs: 'once' }
			],
			[
				'an abandoning outcome',
				{ kind: 'outcome', signal: 'mr_closed', then: 'abort', start: 'manual' },
				{ opens: 'mr_closed', starts: 'when you click Run now', runs: 'once' }
			]
		] as const)('words %s', (_, over, words) => {
			show(over);

			expect(fact('Opens on')).toBe(words.opens);
			expect(fact('Starts')).toBe(words.starts);
			expect(fact('Runs')).toBe(words.runs);
		});

		it('uses the signal’s title when it has one', () => {
			show({ signal: 'ci_failed' }, { titles: { ci_failed: 'CI failing' } });
			expect(fact('Opens on')).toBe('CI failing');
		});

		it.each([
			['done', 'done', 'ends the run: done'],
			['abandons', 'abort', 'abandons the run']
		] as const)('says how an outcome ends the run: %s', (_, then, words) => {
			show({ kind: 'outcome', then });
			expect(within(card()).getByText(words)).toHaveAttribute(
				'title',
				expect.stringMatching(/^An outcome fires by itself/)
			);
		});
	});

	describe('its description', () => {
		it('shows it', () => {
			show({ description: 'Fixes the build when CI fails.' });
			expect(screen.getByText('Fixes the build when CI fails.')).toBeInTheDocument();
		});

		it('leaves it out when there is none', () => {
			show();
			expect(card().querySelector('p')).toBeNull();
		});
	});

	describe('where it stands', () => {
		it('says it is running', () => {
			show({ live: {} });
			expect(within(card()).getByText('running now')).toBeInTheDocument();
		});

		it('says where it is parked', () => {
			show({ live: { parkedAt: 'review' } });
			expect(within(card()).getByText('parked at review')).toBeInTheDocument();
		});

		it('says its signal is pending, and why, for what is new only', () => {
			show({ holds: [hold('lint failed', true), hold('unit failed', false)] });

			expect(within(card()).getByText('pending')).toBeInTheDocument();
			const list = within(card()).getByRole('list');
			expect(
				within(list)
					.getAllByRole('listitem')
					.map((li) => li.textContent)
			).toEqual(['unit failed']);
		});

		it('flags nothing when every hold was seen', () => {
			show({ holds: [hold('lint failed', true)] });

			expect(screen.queryByText('pending')).not.toBeInTheDocument();
			expect(screen.queryByRole('list')).not.toBeInTheDocument();
			expect(within(card()).getByText('idle')).toBeInTheDocument();
		});

		it('says it reached its max', () => {
			show({}, { spent: true });
			expect(within(card()).getByText('max reached')).toBeInTheDocument();
		});

		it('says it is idle', () => {
			show();
			expect(within(card()).getByText('idle')).toBeInTheDocument();
		});
	});

	describe('Run now', () => {
		it('is not offered for the main chain, which ran with the run', () => {
			show({ kind: 'main', signal: '' });

			expect(screen.queryByRole('button')).not.toBeInTheDocument();
			expect(
				screen.getByText('It ran when the run started and never runs again — a new run does.')
			).toBeInTheDocument();
		});

		it('is not offered for an outcome: that happens on the repository', () => {
			show({ kind: 'outcome' });

			expect(screen.queryByRole('button')).not.toBeInTheDocument();
			expect(screen.getByText('happens on the repository')).toBeInTheDocument();
		});

		it.each([
			['a watcher', {}],
			['an outcome', { kind: 'outcome' }]
		] as const)('offers nothing for %s once the run is over', (_, over) => {
			show(over, { ended: true });

			expect(screen.queryByRole('button')).not.toBeInTheDocument();
			expect(screen.queryByText('happens on the repository')).not.toBeInTheDocument();
		});

		it('starts it when no chain holds the run', async () => {
			const onrun = vi.fn();
			show({}, { onrun });

			expect(runNow()).toBeEnabled();
			expect(runNow()).toHaveAttribute('title', 'Start it now, whether or not its signal holds');
			expect(
				within(card()).getByText('Start it now, whether or not its signal holds')
			).toBeInTheDocument();

			await userEvent.click(runNow());

			expect(onrun).toHaveBeenCalledOnce();
		});

		it.each([
			['another chain holds the run', {}, { free: false }, 'Another chain holds the run'],
			[
				'it has a run of its own',
				{ live: { parkedAt: 'review' } },
				{ free: false },
				'This watcher has a run in progress or parked'
			],
			['it reached its max', {}, { spent: true }, 'This watcher reached its max'],
			['a watcher is starting', {}, { locked: true }, 'A watcher is starting']
		] as const)('waits while %s, and says so', (_, over, props, why) => {
			show(over, props);

			expect(runNow()).toBeDisabled();
			expect(runNow()).toHaveAttribute('title', why);
			expect(within(card()).getByText(why)).toBeInTheDocument();
		});

		it('is busy while this one starts', () => {
			show({}, { busy: true, locked: true });
			expect(runNow()).toHaveAttribute('aria-busy', 'true');
		});

		it('is the primary action when it can start and its signal is pending', () => {
			show({ holds: [hold('unit failed', false)] });
			expect(runNow()).toHaveClass('btn-primary');
		});

		it.each<[string, Partial<RunPhase>, Partial<Props>]>([
			['nothing is pending', {}, {}],
			['only seen holds', { holds: [hold('lint failed', true)] }, {}],
			['it cannot start', { holds: [hold('unit failed', false)] }, { free: false }]
		])('stays secondary with %s', (_, over, props) => {
			show(over, props);
			expect(runNow()).not.toHaveClass('btn-primary');
		});
	});
});
