import { render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { RunMergeRequest } from '$lib/domain';
import { aRun } from '$test/fixtures';
import { aHold } from '$test/fixtures/run-components';
import { text } from '$test/helpers';
import RunRow from './RunRow.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const NOW = new Date('2026-10-31T12:00:00Z');
const hoursAgo = (h: number) => new Date(NOW.getTime() - h * 3_600_000).toISOString();
const second = (container: HTMLElement) => container.querySelector('.mid .s');

describe('RunRow', () => {
	beforeEach(() => {
		vi.useFakeTimers({ toFake: ['Date'] });
		vi.setSystemTime(NOW);
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it('opens the run from its title', () => {
		render(RunRow, { run: aRun({ id: '01ABC', title: 'Fix the router' }) });

		expect(screen.getByRole('link', { name: 'Fix the router' })).toHaveAttribute(
			'href',
			'/runs/01ABC'
		);
	});

	it('names an untitled run', () => {
		render(RunRow, { run: aRun({ title: '' }) });
		expect(screen.getByRole('link', { name: 'Untitled run' })).toBeInTheDocument();
	});

	describe('live', () => {
		it('shows the state as a dot, the stage, the id and the age', () => {
			const { container } = render(RunRow, {
				run: aRun({
					state: 'running',
					currentStage: 'build',
					updatedAt: hoursAgo(2)
				})
			});

			expect(screen.getByRole('img', { name: 'Running' })).toBeInTheDocument();
			expect(second(container)).toHaveTextContent('Running · build');
			expect(screen.getByText('r_abcd')).toBeInTheDocument();
			expect(screen.getByText('2h')).toBeInTheDocument();
		});

		it('leaves the stage out when there is none', () => {
			const { container } = render(RunRow, { run: aRun({ state: 'pending' }) });
			expect(second(container)?.textContent?.trim()).toBe('Pending');
		});

		it('lets the caller replace the stage', () => {
			const { container } = render(RunRow, {
				run: aRun({ currentStage: 'build' }),
				detail: 'build · attempt 2'
			});
			expect(second(container)).toHaveTextContent('Running · build · attempt 2');
		});

		it('shows what the caller gives even with no stage', () => {
			const { container } = render(RunRow, { run: aRun(), detail: 'queued 3rd' });
			expect(second(container)).toHaveTextContent('Running · queued 3rd');
		});

		it('tells a run in your hands', () => {
			const { container } = render(RunRow, { run: aRun({ hold: aHold() }) });
			expect(second(container)).toHaveTextContent('Running · in your hands');
		});

		// Svelte trims the whitespace at a block's edges: not "build· in your hands".
		it('keeps a space between the stage and "in your hands"', () => {
			const { container } = render(RunRow, {
				run: aRun({ currentStage: 'build', hold: aHold() })
			});
			expect(second(container)).toHaveTextContent('Running · build · in your hands');
		});
	});

	describe('finished', () => {
		const finished = (over: Parameters<typeof aRun>[0]) =>
			render(RunRow, { run: aRun(over), variant: 'finished' });

		it.each([
			['completed', 'Completed'],
			['failed', 'Failed'],
			['abandoned', 'Abandoned'],
			['archived', 'Archived']
		] as const)('marks a %s run and says how long ago', (state, word) => {
			const { container } = finished({ state, updatedAt: hoursAgo(5) });

			expect(screen.getByRole('img', { name: word })).toBeInTheDocument();
			expect(second(container)).toHaveTextContent(`${word} · 5h ago`);
		});

		it('picks the mark from the outcome', () => {
			const { unmount } = finished({ state: 'completed' });
			expect(screen.getByRole('img', { name: 'Completed' })).toHaveClass('ok');
			unmount();

			const failed = finished({ state: 'failed' });
			expect(screen.getByRole('img', { name: 'Failed' })).toHaveClass('bad');
			failed.unmount();

			finished({ state: 'abandoned' });
			expect(screen.getByRole('img', { name: 'Abandoned' })).toHaveClass('muted');
		});

		it('links the merge request instead of the id and age', () => {
			const mrs = [{ repo: 'repo', url: 'https://git.test/20', iid: 20 } as RunMergeRequest];
			finished({ state: 'completed', mrs });

			expect(screen.getByRole('link', { name: /^MR/ })).toHaveAttribute(
				'href',
				'https://git.test/20'
			);
			expect(screen.queryByText('r_abcd')).not.toBeInTheDocument();
		});

		it('does not call a finished run yours', () => {
			const { container } = finished({ state: 'completed', hold: aHold() });
			expect(second(container)).not.toHaveTextContent('in your hands');
		});
	});

	it('gives the trailing slot to the caller', () => {
		render(RunRow, { run: aRun(), trailing: text('$0.42') });

		expect(screen.getByText('$0.42')).toBeInTheDocument();
		expect(screen.queryByText('r_abcd')).not.toBeInTheDocument();
	});
});
