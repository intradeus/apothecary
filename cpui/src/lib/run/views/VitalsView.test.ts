import { render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api';
import { aLiveRun, aSample, aViewStore } from '$test/fixtures/run-views';
import VitalsView from './VitalsView.svelte';

vi.mock('$lib/api', () => ({ api: { runs: { resources: vi.fn() } } }));

const two = [
	aSample({ at: '2026-10-31T12:00:00Z', memUsed: 1024 * 1024 }),
	aSample({ at: '2026-10-31T12:00:10Z', memUsed: 2 * 1024 * 1024 })
];

describe('VitalsView', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		vi.mocked(api.runs.resources).mockResolvedValue([]);
	});

	it('holds the resource feed while on screen, and lets go on hide', async () => {
		const rs = aViewStore(aLiveRun());
		const release = vi.fn();
		const hold = vi.spyOn(rs, 'useResources').mockReturnValue(release);

		const { rerender } = render(VitalsView, { rs, visible: true });
		expect(hold).toHaveBeenCalledOnce();

		await rerender({ visible: false });
		expect(release).toHaveBeenCalledOnce();
	});

	it('lets go when the window closes', () => {
		const rs = aViewStore(aLiveRun());
		const release = vi.fn();
		vi.spyOn(rs, 'useResources').mockReturnValue(release);

		const { unmount } = render(VitalsView, { rs, visible: true });
		unmount();

		expect(release).toHaveBeenCalledOnce();
	});

	it('reads nothing while hidden', () => {
		render(VitalsView, { rs: aViewStore(aLiveRun()), visible: false });
		expect(api.runs.resources).not.toHaveBeenCalled();
	});

	it('reads the samples as soon as it is shown', () => {
		render(VitalsView, { rs: aViewStore(aLiveRun()), visible: true });
		expect(api.runs.resources).toHaveBeenCalledExactlyOnceWith('01TESTRUN0000000000000ABCD');
	});

	it('says samples are on their way until two have landed', () => {
		render(VitalsView, {
			rs: aViewStore(aLiveRun(), { samples: [aSample()] }),
			visible: true
		});
		expect(screen.getByText('Collecting samples…')).toBeInTheDocument();
	});

	it('draws the store’s samples', () => {
		vi.mocked(api.runs.resources).mockResolvedValue(two);
		render(VitalsView, { rs: aViewStore(aLiveRun(), { samples: two }), visible: true });

		expect(screen.getByText('CPU')).toBeInTheDocument();
		expect(screen.getByText('7 processes · sampled every ~10s')).toBeInTheDocument();
		expect(screen.getByText(/^2\.0 MB/)).toHaveTextContent(/\(50%\)$/);
	});

	it('follows the samples as they land', async () => {
		const rs = aViewStore(aLiveRun());
		render(VitalsView, { rs, visible: true });
		await tick();

		rs.samples = two;
		await tick();

		expect(screen.getByText('CPU')).toBeInTheDocument();
		expect(screen.queryByText('Collecting samples…')).not.toBeInTheDocument();
	});
});
