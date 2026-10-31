import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { aRun, aRunStore } from '$test/fixtures';
import NoWorkspace from './NoWorkspace.svelte';

const KEEPS = 'The Record keeps every snapshot and log this run captured.';
const APPEARS = 'It appears here once the run has provisioned one.';

describe('NoWorkspace', () => {
	beforeEach(() => {
		vi.useFakeTimers({ toFake: ['Date'] });
		vi.setSystemTime(new Date('2026-10-31T12:00:00Z'));
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	it('says when the workspace was destroyed', () => {
		const rs = aRunStore(aRun({ state: 'completed' }), {
			destroyedAt: '2026-10-31T09:00:00Z'
		});
		render(NoWorkspace, { rs, onrecord: vi.fn() });

		expect(screen.getByText('Workspace destroyed 3h ago')).toBeInTheDocument();
		expect(screen.getByText(KEEPS)).toBeInTheDocument();
	});

	it('puts the destruction before the archive', () => {
		const rs = aRunStore(aRun({ state: 'archived' }), { destroyedAt: '2026-10-31T09:00:00Z' });
		render(NoWorkspace, { rs, onrecord: vi.fn() });

		expect(screen.getByText('Workspace destroyed 3h ago')).toBeInTheDocument();
		expect(screen.queryByText('Workspace archived away')).not.toBeInTheDocument();
	});

	it('says an archived run took its workspace along', () => {
		render(NoWorkspace, { rs: aRunStore(aRun({ state: 'archived' })), onrecord: vi.fn() });

		expect(screen.getByText('Workspace archived away')).toBeInTheDocument();
		expect(screen.getByText(KEEPS)).toBeInTheDocument();
	});

	it('promises one to a run that is still getting there', () => {
		render(NoWorkspace, { rs: aRunStore(aRun({ state: 'provisioning' })), onrecord: vi.fn() });

		expect(screen.getByText('No workspace yet')).toBeInTheDocument();
		expect(screen.getByText(APPEARS)).toBeInTheDocument();
	});

	it('promises nothing to a finished run that had one', () => {
		const rs = aRunStore(aRun({ state: 'failed', envId: 'env-1' }));
		render(NoWorkspace, { rs, onrecord: vi.fn() });

		expect(screen.getByText('No live workspace')).toBeInTheDocument();
		expect(screen.getByText(KEEPS)).toBeInTheDocument();
	});

	it('promises nothing to a finished run that never had one', () => {
		render(NoWorkspace, { rs: aRunStore(aRun({ state: 'failed' })), onrecord: vi.fn() });

		expect(screen.getByText('No live workspace')).toBeInTheDocument();
		expect(screen.queryByText(APPEARS)).not.toBeInTheDocument();
	});

	it('says no live workspace while the run has an env behind it', () => {
		const rs = aRunStore(aRun({ state: 'running', envId: 'env-1' }));
		render(NoWorkspace, { rs, onrecord: vi.fn() });

		expect(screen.getByText('No live workspace')).toBeInTheDocument();
		expect(screen.getByText(KEEPS)).toBeInTheDocument();
	});

	it('copes before the run has loaded', () => {
		render(NoWorkspace, {
			rs: aRunStore(aRun(), { run: null, terminal: false }),
			onrecord: vi.fn()
		});

		expect(screen.getByText('No live workspace')).toBeInTheDocument();
	});

	it('opens the Record', async () => {
		vi.useRealTimers();
		const onrecord = vi.fn();
		render(NoWorkspace, { rs: aRunStore(), onrecord });

		await userEvent.click(screen.getByRole('button', { name: 'Open the Record' }));

		expect(onrecord).toHaveBeenCalledOnce();
	});
});
