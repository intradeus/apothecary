import { describe, expect, it } from 'vitest';
import { aUnit } from '$test/fixtures/swarm';
import { layoutSwarm, ringOf, spiral, UNIT_REACH, UNIT_STEP, type Point } from './hexLayout';

const names: Record<string, string> = { a: 'alpha', b: 'bravo', c: 'charlie', d: 'delta' };
const nameOf = (id: string) => names[id] ?? id;
const dist = (p: Point, q: Point) => Math.hypot(p.x - q.x, p.y - q.y);
const unitsIn = (projectId: string, n: number) =>
	Array.from({ length: n }, (_, i) =>
		aUnit({ runId: `${projectId}${i}`, projectId, createdAt: `2026-10-31T12:00:0${i}Z` })
	);

describe('the hex spiral', () => {
	it('starts at the centre and walks each ring once', () => {
		const cells = spiral(19);
		expect(cells[0]).toEqual([0, 0]);
		expect(new Set(cells.map((c) => c.join())).size).toBe(19);
		// Every cell of ring 1 neighbours the centre (axial distance 1).
		for (const [q, r] of cells.slice(1, 7))
			expect(Math.max(Math.abs(q), Math.abs(r), Math.abs(-q - r))).toBe(1);
		expect(
			cells.slice(7).every(([q, r]) => Math.max(Math.abs(q), Math.abs(r), Math.abs(-q - r)) === 2)
		).toBe(true);
	});

	it('knows which ring a cell is on', () => {
		expect([0, 1, 6, 7, 18, 19].map(ringOf)).toEqual([0, 1, 1, 2, 2, 3]);
	});
});

describe('the map', () => {
	it('lays the same board out the same way, whatever order it arrives in', () => {
		const units = [...unitsIn('b', 2), ...unitsIn('a', 3), ...unitsIn('c', 1)];
		const one = layoutSwarm(units, nameOf);
		const two = layoutSwarm([...units].reverse(), nameOf);
		expect(two).toEqual(one);
		expect(one.sectors.map((s) => s.name)).toEqual(['alpha', 'bravo', 'charlie']);
		expect(one.sectors[0].center).toEqual({ x: 0, y: 0 });
		expect(one.sectors[0].units.map((u) => u.runId)).toEqual(['a0', 'a1', 'a2']);
	});

	it('never overlaps two sectors', () => {
		const ids = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i'];
		const { sectors } = layoutSwarm(
			ids.flatMap((id, i) => unitsIn(id, 1 + (i % 4))),
			nameOf
		);
		expect(sectors).toHaveLength(ids.length);
		for (const s of sectors)
			for (const o of sectors)
				if (s !== o)
					expect(dist(s.center, o.center)).toBeGreaterThanOrEqual(Math.sqrt(3) * s.radius);
	});

	it('opens a second ring of slots past the first one’s seven', () => {
		const seven = layoutSwarm(unitsIn('a', 7), nameOf).sectors[0];
		const eight = layoutSwarm(unitsIn('a', 8), nameOf).sectors[0];
		const reach = (s: typeof seven) => Math.max(...s.units.map((u) => dist(u.at, s.center)));
		expect(reach(seven)).toBeLessThan(UNIT_STEP * 1.5);
		expect(reach(eight)).toBeGreaterThan(UNIT_STEP * 1.5);
		expect(eight.radius).toBeGreaterThan(seven.radius);
	});

	it('keeps every unit inside its sector', () => {
		for (const n of [1, 7, 8, 19]) {
			const s = layoutSwarm(unitsIn('a', n), nameOf).sectors[0];
			const inradius = (s.radius * Math.sqrt(3)) / 2;
			for (const u of s.units) expect(dist(u.at, s.center) + UNIT_REACH).toBeLessThan(inradius);
		}
	});

	it('frames every sector, and an empty board still has a frame', () => {
		const { sectors, viewBox } = layoutSwarm([...unitsIn('a', 1), ...unitsIn('b', 1)], nameOf);
		for (const p of sectors.flatMap((s) => s.corners)) {
			expect(p.x).toBeGreaterThan(viewBox.x);
			expect(p.y).toBeGreaterThan(viewBox.y);
			expect(p.x).toBeLessThan(viewBox.x + viewBox.width);
			expect(p.y).toBeLessThan(viewBox.y + viewBox.height);
		}
		const empty = layoutSwarm([], nameOf);
		expect(empty.sectors).toEqual([]);
		expect(empty.viewBox.width).toBeGreaterThan(0);
	});
});
