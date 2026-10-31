import { render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fleet } from '$lib/app/fleet.svelte';
import { aPing, aProject, aRun } from '$test/fixtures';
import NeedsYou from './NeedsYou.svelte';

vi.mock('$lib/api', () => ({ api: {} }));

const at = (day: number) => `2026-10-${String(day).padStart(2, '0')}T12:00:00Z`;
const questions = () => screen.getAllByRole('link', { name: /\?$/ });

beforeEach(() => {
	vi.useFakeTimers({ now: new Date('2026-10-31T12:00:00Z') });
	fleet.reset();
	fleet.projects = [aProject({ id: 'p1', name: 'billing' })];
	fleet.runs = [
		aRun({ id: '01RUNAAAA', title: 'Fix the invoice', projectId: 'p1' }),
		aRun({ id: '01RUNBBBB', title: 'Add caching', projectId: 'p1' })
	];
});
afterEach(() => {
	vi.useRealTimers();
});

describe('NeedsYou', () => {
	it('counts what is pending and links the inbox', () => {
		const pings = [aPing({ id: 'a', raisedAt: at(1) }), aPing({ id: 'b', raisedAt: at(2) })];
		render(NeedsYou, { pings });
		expect(screen.getByRole('heading', { name: 'Awaiting your hand' })).toBeInTheDocument();
		expect(screen.getByText('2')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /Open inbox/ })).toHaveAttribute('href', '/inbox');
	});

	it('lists the runs oldest first', () => {
		render(NeedsYou, {
			pings: [
				aPing({ id: 'new', runId: '01RUNBBBB', raisedAt: at(5), question: 'Newer?' }),
				aPing({ id: 'old', runId: '01RUNAAAA', raisedAt: at(1), question: 'Older?' })
			]
		});
		expect(questions().map((a) => a.textContent)).toEqual(['Older?', 'Newer?']);
	});

	it("answers a run's newest ping first, the rest queued behind it", () => {
		render(NeedsYou, {
			pings: [
				aPing({ id: 'first', runId: '01RUNAAAA', raisedAt: at(1), question: 'First?' }),
				aPing({ id: 'second', runId: '01RUNAAAA', raisedAt: at(2), question: 'Second?' }),
				aPing({ id: 'third', runId: '01RUNAAAA', raisedAt: at(3), question: 'Third?' })
			]
		});
		expect(questions().map((a) => a.textContent)).toEqual(['Third?']);
		expect(questions()[0]).toHaveAttribute('href', '/inbox/third');
		expect(screen.getByText(/2 more queued/)).toBeInTheDocument();
	});

	it("shows the run, its stage, its workshop and the ping's age", () => {
		render(NeedsYou, {
			pings: [aPing({ runId: '01RUNAAAA', raisedAt: at(29), stageId: 'build' })]
		});
		expect(screen.getByText(/Fix the invoice/)).toBeInTheDocument();
		expect(screen.getByText('build')).toBeInTheDocument();
		expect(screen.getByText('r_aaaa')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'billing' })).toHaveAttribute('href', '/w/p1');
		expect(screen.getByText('2d')).toBeInTheDocument();
	});

	it('falls back for a ping with no wording, and a run it does not know', () => {
		render(NeedsYou, { pings: [aPing({ runId: 'gone', raisedAt: at(1), question: '' })] });
		expect(screen.getByRole('link', { name: 'A question for you' })).toBeInTheDocument();
		expect(screen.getByText(/· run/)).toBeInTheDocument();
	});

	it('calls the row to the action its ping takes', () => {
		render(NeedsYou, {
			pings: [
				aPing({ id: 'g', runId: '01RUNAAAA', raisedAt: at(1), answerKind: 'yes_no' }),
				aPing({
					id: 'q',
					runId: '01RUNBBBB',
					raisedAt: at(2),
					type: 'clarification',
					answerKind: 'form'
				})
			]
		});
		expect(screen.getByRole('link', { name: 'Decide' })).toHaveAttribute('href', '/inbox/g');
		expect(screen.getByRole('link', { name: 'Answer' })).toHaveAttribute('href', '/inbox/q');
	});
});
