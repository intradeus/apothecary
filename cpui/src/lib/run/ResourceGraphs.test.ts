import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { fmtTime } from '$lib/core/format';
import type { ResourceSample } from '$lib/domain';
import { aSample } from '$test/fixtures/run-components';
import { rect } from '$test/helpers';
import ResourceGraphs from './ResourceGraphs.svelte';

const T0 = Date.parse('2026-10-31T12:00:00Z');
const at = (s: number) => new Date(T0 + s * 1000).toISOString();
const GiB = 1024 ** 3;

/** Samples ten seconds apart. */
const series = (...over: Partial<ResourceSample>[]) =>
	over.map((o, i) => aSample({ at: at(i * 10), ...o }));

/** The chart under this title: its readout text and its scale. */
function chart(title: string) {
	const box = screen.getByText(title).closest('.chart') as HTMLElement;
	return {
		box,
		vals: box.querySelector('.vals') as HTMLElement,
		max: box.querySelector('.ymax')
	};
}
/** A plot wide enough to draw: jsdom lays nothing out. */
const wide = () => vi.spyOn(Element.prototype, 'clientWidth', 'get').mockReturnValue(400);

describe('ResourceGraphs', () => {
	describe('before there is a line to draw', () => {
		it.each([0, 1])('asks for patience with %i samples', (n) => {
			render(ResourceGraphs, { samples: series(...Array(n).fill({})) });

			expect(screen.getByText('Collecting samples…')).toBeInTheDocument();
			expect(screen.getByText('One every ~10s while the workspace is up.')).toBeInTheDocument();
			expect(screen.queryByText('CPU')).not.toBeInTheDocument();
		});

		it('draws from the second sample', () => {
			render(ResourceGraphs, { samples: series({}, {}) });

			expect(screen.queryByText('Collecting samples…')).not.toBeInTheDocument();
			expect(screen.getByText('CPU')).toBeInTheDocument();
			expect(screen.getByText('Memory')).toBeInTheDocument();
			expect(screen.getByText('Network')).toBeInTheDocument();
		});
	});

	describe('the header', () => {
		it('counts the processes, singular and plural', async () => {
			const { rerender } = render(ResourceGraphs, { samples: series({ pids: 1 }) });
			expect(screen.getByText('1 process · sampled every ~10s')).toBeInTheDocument();

			await rerender({ samples: series({ pids: 12 }) });
			expect(screen.getByText('12 processes · sampled every ~10s')).toBeInTheDocument();
		});

		it('says how often it samples when the count is unknown', () => {
			render(ResourceGraphs, { samples: series({ pids: 0 }) });
			expect(screen.getByText('sampled every ~10s')).toBeInTheDocument();
		});

		it.each([
			[20, 'last minute'],
			[300, 'last 5m'],
			[7200, 'last 2h']
		])('names a window of %i seconds', (s, label) => {
			render(ResourceGraphs, { samples: [aSample({ at: at(0) }), aSample({ at: at(s) })] });
			expect(screen.getByText(label)).toBeInTheDocument();
		});
	});

	describe('CPU', () => {
		it('shows % of the machine when the core count is known', () => {
			render(ResourceGraphs, {
				samples: series({ cpuPerc: 100, cpus: 4 }, { cpuPerc: 200, cpus: 4 })
			});
			const { vals, max } = chart('CPU');

			expect(vals).toHaveTextContent('50%');
			expect(vals).toHaveTextContent('% of 4 cores');
			expect(max).toHaveTextContent('50%');
		});

		it('says one core in the singular', () => {
			render(ResourceGraphs, { samples: series({ cpus: 1 }, { cpus: 1 }) });
			expect(screen.getByText('% of 1 core')).toBeInTheDocument();
		});

		it('never scales past a whole machine', () => {
			render(ResourceGraphs, {
				samples: series({ cpuPerc: 800, cpus: 4 }, { cpuPerc: 800, cpus: 4 })
			});
			expect(chart('CPU').max).toHaveTextContent('100%');
		});

		it('rounds the scale up to a clean step', () => {
			render(ResourceGraphs, {
				samples: series({ cpuPerc: 30, cpus: 10 }, { cpuPerc: 30, cpus: 10 })
			});
			expect(chart('CPU').max).toHaveTextContent('5.0%');
		});

		it('shows raw per-core % with hundreds on the scale when cores are unknown', () => {
			render(ResourceGraphs, { samples: series({ cpuPerc: 50 }, { cpuPerc: 250 }) });
			const { vals, max } = chart('CPU');

			expect(vals).toHaveTextContent('250%');
			expect(vals).not.toHaveTextContent(/core/);
			expect(max).toHaveTextContent('300%');
		});

		it('keeps at least 100 on that scale', () => {
			render(ResourceGraphs, { samples: series({ cpuPerc: 5 }, { cpuPerc: 8 }) });
			expect(chart('CPU').max).toHaveTextContent('100%');
		});
	});

	describe('memory', () => {
		it('reads against the limit, as a share of it', () => {
			render(ResourceGraphs, {
				samples: series(
					{ memUsed: GiB, memLimit: 4 * GiB },
					{ memUsed: 2 * GiB, memLimit: 4 * GiB }
				)
			});
			const { vals, max } = chart('Memory');

			expect(vals).toHaveTextContent('2.00 GB');
			expect(vals).toHaveTextContent('(50%)');
			expect(max).toHaveTextContent('4.00 GB');
		});

		it('picks a clean scale when there is no limit, and no share', () => {
			render(ResourceGraphs, {
				samples: series({ memUsed: 1500 * 1024 }, { memUsed: 3 * 1024 * 1024 })
			});
			const { vals, max } = chart('Memory');

			expect(vals).toHaveTextContent('3.0 MB');
			expect(vals).not.toHaveTextContent('%');
			expect(max).toHaveTextContent('5.0 MB');
		});

		it('moves to the next unit rather than read 1000', () => {
			render(ResourceGraphs, {
				samples: series({ memUsed: 700 * 1024 }, { memUsed: 900 * 1024 })
			});
			expect(chart('Memory').max).toHaveTextContent('1.0 MB');
		});
	});

	describe('network', () => {
		it('turns the cumulative counters into rates', () => {
			render(ResourceGraphs, {
				samples: series(
					{ netRx: 0, netTx: 0 },
					{ netRx: 10 * 1024, netTx: 20 * 1024 },
					{ netRx: 30 * 1024, netTx: 30 * 1024 }
				)
			});
			const { vals, max } = chart('Network');

			expect(vals).toHaveTextContent('in 2 KB/s');
			expect(vals).toHaveTextContent('out 1 KB/s');
			expect(max).toHaveTextContent('2 KB/s');
		});

		it('reads zero across a counter reset', () => {
			render(ResourceGraphs, {
				samples: series({ netRx: 5000, netTx: 5000 }, { netRx: 100, netTx: 100 })
			});
			expect(chart('Network').vals).toHaveTextContent('in 0 B/s');
		});
	});

	describe('the plots', () => {
		const samples = series(
			{ cpuPerc: 20, memUsed: 1 * GiB, netRx: 0, netTx: 0 },
			{ cpuPerc: 40, memUsed: 2 * GiB, netRx: 1024, netTx: 2048 },
			{ cpuPerc: 80, memUsed: 3 * GiB, netRx: 3072, netTx: 2048 }
		);
		const plot = (title: string) => within(chart(title).box).queryByRole('img');

		it('waits for a width', () => {
			render(ResourceGraphs, { samples });
			expect(plot('CPU')).not.toBeInTheDocument();
		});

		it('labels each plot and draws it across the width', () => {
			wide();
			render(ResourceGraphs, { samples });

			const svg = screen.getByRole('img', { name: 'CPU usage, last minute' });
			expect(svg).toHaveAttribute('width', '400');
			const line = svg.querySelector('path[fill="none"]');
			expect(line?.getAttribute('d')).toMatch(/^M1\.0,[\d.]+L200\.0,[\d.]+L399\.0,[\d.]+$/);
		});

		it('starts the network line at its first rate', () => {
			wide();
			render(ResourceGraphs, { samples });

			const line = plot('Network')?.querySelector('path[fill="none"]');
			// The first sample has no rate, so it opens at the second.
			expect(line?.getAttribute('d')).toMatch(/^M200\.0,[\d.]+L399\.0,[\d.]+$/);
		});

		it('breaks the line across a gap in the samples', () => {
			wide();
			render(ResourceGraphs, {
				samples: [
					aSample({ at: at(0), netRx: 0 }),
					aSample({ at: at(10), netRx: 1024 }),
					aSample({ at: at(20), netRx: 2048 }),
					// Same timestamp: no rate here.
					aSample({ at: at(20), netRx: 2048 }),
					aSample({ at: at(30), netRx: 4096 })
				]
			});

			const d = plot('Network')?.querySelector('path[fill="none"]')?.getAttribute('d');
			expect(d?.match(/M/g)).toHaveLength(2);
		});

		it('washes under the solid lines, not the dashed one', () => {
			wide();
			render(ResourceGraphs, { samples });

			const paths = [...(plot('Network')?.querySelectorAll('path') ?? [])];
			expect(paths.filter((p) => p.getAttribute('stroke-dasharray'))).toHaveLength(1);
			expect(paths.filter((p) => p.getAttribute('opacity') === '0.16')).toHaveLength(1);
			expect(paths.find((p) => p.getAttribute('opacity') === '0.16')?.getAttribute('d')).toMatch(
				/Z$/
			);
		});

		it('does not wash a lone point', () => {
			wide();
			render(ResourceGraphs, {
				samples: series({ netRx: 0 }, { netRx: 1024 })
			});

			const wash = plot('Network')?.querySelector('path[opacity="0.16"]');
			expect(wash?.getAttribute('d')).toBe('');
		});
	});

	describe('the crosshair', () => {
		const samples = series(
			{ cpuPerc: 10, memUsed: 1024 },
			{ cpuPerc: 20, memUsed: 2048 },
			{ cpuPerc: 90, memUsed: 3072 }
		);

		async function hover(clientX: number) {
			const svg = screen.getByRole('img', { name: /^CPU/ });
			vi.spyOn(svg, 'getBoundingClientRect').mockReturnValue(rect(100, 0, 400, 82));
			await fireEvent.mouseMove(svg, { clientX });
			return svg;
		}

		it('reads the sample under the pointer in every chart', async () => {
			wide();
			render(ResourceGraphs, { samples });
			expect(chart('CPU').vals).toHaveTextContent('90%');

			await hover(310); // just past the middle: the second sample

			expect(chart('CPU').vals).toHaveTextContent('20%');
			expect(chart('Memory').vals).toHaveTextContent('2 KB');
		});

		it('picks the nearest sample', async () => {
			wide();
			render(ResourceGraphs, { samples });

			await hover(190); // nearer the first than the second

			expect(chart('CPU').vals).toHaveTextContent('10%');
		});

		it('says when that sample was taken', async () => {
			wide();
			render(ResourceGraphs, { samples });

			await hover(300);

			expect(screen.getByText(`at ${fmtTime(at(10))}`)).toBeInTheDocument();
			expect(screen.queryByText('last minute')).not.toBeInTheDocument();
		});

		it('marks the sample on every plot', async () => {
			wide();
			render(ResourceGraphs, { samples });

			await hover(300);

			expect(screen.getByRole('img', { name: /^CPU/ }).querySelectorAll('circle')).toHaveLength(1);
			expect(screen.getByRole('img', { name: /^Memory/ }).querySelectorAll('circle')).toHaveLength(
				1
			);
		});

		it('dashes a sample with no rate', async () => {
			wide();
			render(ResourceGraphs, { samples });

			await hover(100); // the first sample has no rate

			const network = chart('Network');
			expect(network.vals).toHaveTextContent('in —');
			expect(network.box.querySelector('circle')).not.toBeInTheDocument();
		});

		it('goes back to now when the pointer leaves', async () => {
			wide();
			render(ResourceGraphs, { samples });
			const svg = await hover(300);

			await fireEvent.mouseLeave(svg);

			expect(chart('CPU').vals).toHaveTextContent('90%');
			expect(screen.getByText('last minute')).toBeInTheDocument();
			expect(svg.querySelector('circle')).not.toBeInTheDocument();
		});
	});
});
