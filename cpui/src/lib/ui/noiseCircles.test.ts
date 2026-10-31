import { beforeEach, describe, expect, it, vi } from 'vitest';
import { CIRCLE_COUNT, noiseCircles } from './noiseCircles';

type Circle = ReturnType<typeof noiseCircles>['circles'][number];

const pairs = (circles: Circle[]) =>
	circles.flatMap((a, i) => circles.slice(i + 1).map((b) => [a, b] as const));
/** How far inside the closest allowed distance the worst pair is; <= 0 is clear. */
const worstOverlap = (circles: Circle[]) =>
	Math.max(
		...pairs(circles).map(([a, b]) => Math.max(a.r, b.r) - Math.hypot(b.x - a.x, b.y - a.y))
	);
const positions = (circles: Circle[]) => circles.map(({ x, y }) => ({ x, y }));

describe('noiseCircles', () => {
	beforeEach(() => {
		vi.spyOn(Math, 'random').mockReturnValue(0.3);
	});

	it('makes the advertised number of circles, largest first', () => {
		const { circles } = noiseCircles();
		expect(circles).toHaveLength(CIRCLE_COUNT);
		expect(circles.map((c) => c.r)).toEqual([344, 280, 240, 208]);
	});

	it('starts from the same place for the same random start', () => {
		expect(positions(noiseCircles().circles)).toEqual(positions(noiseCircles().circles));
	});

	it('starts somewhere else for another', () => {
		const one = positions(noiseCircles().circles);
		vi.spyOn(Math, 'random').mockReturnValue(0.8);
		expect(positions(noiseCircles().circles)).not.toEqual(one);
	});

	it.each([0, 0.25, 0.5, 0.75, 0.999])(
		'settles with no two centres inside the limit, from a start of %d',
		(start) => {
			vi.spyOn(Math, 'random').mockReturnValue(start);
			expect(worstOverlap(noiseCircles().circles)).toBeLessThanOrEqual(0.5);
		}
	);

	it('holds the limit along the way', () => {
		const sim = noiseCircles();
		for (let i = 0; i < 300; i++) {
			sim.advance(1);
			expect(worstOverlap(sim.circles)).toBeLessThanOrEqual(0.5);
		}
	});

	it('holds the limit from many different starts', () => {
		let seed = 0;
		vi.spyOn(Math, 'random').mockImplementation(() => (seed = (seed + 0.618034) % 1));

		for (let run = 0; run < 60; run++) {
			const sim = noiseCircles();
			for (let i = 0; i < 20; i++) {
				sim.advance(1);
				expect(worstOverlap(sim.circles)).toBeLessThanOrEqual(0.5);
			}
		}
	});

	it('moves on as time passes', () => {
		const sim = noiseCircles();
		const before = positions(sim.circles);
		sim.advance(5);
		expect(positions(sim.circles)).not.toEqual(before);
	});

	it('stays put for no time at all', () => {
		const sim = noiseCircles();
		const before = positions(sim.circles);
		sim.advance(0);
		expect(positions(sim.circles)).toEqual(before);
	});

	it('takes the same path however the time is cut up', () => {
		const whole = noiseCircles();
		const cut = noiseCircles();
		whole.advance(0.2);
		for (let i = 0; i < 10; i++) cut.advance(0.02);

		whole.circles.forEach((c, i) => {
			expect(c.x).toBeCloseTo(cut.circles[i].x, 6);
			expect(c.y).toBeCloseTo(cut.circles[i].y, 6);
		});
	});

	it('stays stable through one long jump, as a backgrounded tab makes', () => {
		const sim = noiseCircles();
		sim.advance(600);
		for (const c of sim.circles) {
			expect(Number.isFinite(c.x) && Number.isFinite(c.y)).toBe(true);
			expect(Math.hypot(c.x, c.y)).toBeLessThan(2_000);
		}
	});

	it('drives each circle from its own object, so a renderer can hold them', () => {
		const sim = noiseCircles();
		const held = sim.circles[0];
		sim.advance(1);
		expect(sim.circles[0]).toBe(held);
	});
});
