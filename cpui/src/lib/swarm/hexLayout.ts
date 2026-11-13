// Where everything sits on the war room's map. Each workshop is a hex
// sector on an axial grid, spiralling out from the centre in workshop-name
// order; each of its units takes a slot on a smaller hex spiral inside it,
// oldest at the centre, a second ring opening when the first is full. Pure
// and deterministic: the same board lays out the same way every poll, so
// nothing jumps.

import type { SwarmUnit } from '$lib/domain';

export interface Point {
	x: number;
	y: number;
}

export interface SectorLayout {
	projectId: string;
	name: string;
	/** Its place in the spiral (0 = the centre). */
	index: number;
	center: Point;
	/** Circumradius: centre to corner. */
	radius: number;
	/** The six corners, pointy-top, for an SVG polygon. */
	corners: Point[];
	/** Where its name goes: inside the top edge. */
	label: Point;
	units: { runId: string; at: Point }[];
}

export interface MapLayout {
	sectors: SectorLayout[];
	viewBox: { x: number; y: number; width: number; height: number };
}

/** Distance between two neighbouring unit slots: room for a unit's name
 *  under it without touching its neighbour's. */
export const UNIT_STEP = 96;
/** A unit's reach from its slot: ring, bars and drone orbit. */
export const UNIT_REACH = 30;
/** Room between a sector's edge and its outermost unit, and its name. */
const SECTOR_PAD = 34;
/** Sectors are spaced this much wider than touching. */
const SECTOR_GAP = 1.08;
const MARGIN = 24;
const SQRT3 = Math.sqrt(3);

/** Axial neighbour directions, in spiral order. */
const DIRS: [number, number][] = [
	[1, 0],
	[1, -1],
	[0, -1],
	[-1, 0],
	[-1, 1],
	[0, 1]
];

/** The axial coordinates of the first n cells of a hex spiral. */
export function spiral(n: number): [number, number][] {
	const out: [number, number][] = [[0, 0]];
	for (let ring = 1; out.length < n; ring++) {
		let q = DIRS[4][0] * ring;
		let r = DIRS[4][1] * ring;
		for (let side = 0; side < 6; side++) {
			for (let step = 0; step < ring; step++) {
				out.push([q, r]);
				q += DIRS[side][0];
				r += DIRS[side][1];
			}
		}
	}
	return out.slice(0, n);
}

/** Which ring of a hex spiral its i-th cell is on (0 = the centre). */
export function ringOf(i: number): number {
	let ring = 0;
	while (i > 3 * ring * (ring + 1)) ring++;
	return ring;
}

/** Pointy-top axial → pixel, for cells whose neighbours are `step` apart. */
const toPixel = ([q, r]: [number, number], step: number): Point => ({
	x: step * (q + r / 2),
	y: ((step * SQRT3) / 2) * r
});

const corners = (c: Point, radius: number): Point[] =>
	Array.from({ length: 6 }, (_, i) => {
		const a = (Math.PI / 180) * (60 * i - 90);
		return { x: c.x + radius * Math.cos(a), y: c.y + radius * Math.sin(a) };
	});

/** Lays the board out: sectors by workshop name (then id), units in a
 *  sector by age (then id). Every sector has the radius the most crowded
 *  one needs, so the grid stays regular. */
export function layoutSwarm(
	units: readonly SwarmUnit[],
	nameOf: (projectId: string) => string
): MapLayout {
	const byProject = new Map<string, SwarmUnit[]>();
	for (const u of units) byProject.set(u.projectId, [...(byProject.get(u.projectId) ?? []), u]);
	const order = [...byProject.keys()]
		.map((projectId) => ({ projectId, name: nameOf(projectId) }))
		.sort((a, b) => a.name.localeCompare(b.name) || a.projectId.localeCompare(b.projectId));

	const crowd = Math.max(1, ...[...byProject.values()].map((us) => us.length));
	// The outermost slot sits ringOf × UNIT_STEP from the centre: inside the
	// sector's inscribed circle, so inside the hex.
	const inradius = ringOf(crowd - 1) * UNIT_STEP + UNIT_REACH + SECTOR_PAD;
	const radius = (inradius * 2) / SQRT3;
	const cells = spiral(order.length);

	const sectors = order.map(({ projectId, name }, index): SectorLayout => {
		const center = toPixel(cells[index], SQRT3 * radius * SECTOR_GAP);
		const mine = [...(byProject.get(projectId) ?? [])].sort(
			(a, b) => a.createdAt.localeCompare(b.createdAt) || a.runId.localeCompare(b.runId)
		);
		const slots = spiral(mine.length);
		return {
			projectId,
			name,
			index,
			center,
			radius,
			corners: corners(center, radius),
			label: { x: center.x, y: center.y - inradius + SECTOR_PAD / 2 },
			units: mine.map((u, i) => {
				const p = toPixel(slots[i], UNIT_STEP);
				return { runId: u.runId, at: { x: center.x + p.x, y: center.y + p.y + SECTOR_PAD / 4 } };
			})
		};
	});

	const pts = sectors.flatMap((s) => s.corners);
	if (!pts.length)
		return { sectors, viewBox: { x: -radius, y: -radius, width: 2 * radius, height: 2 * radius } };
	const xs = pts.map((p) => p.x);
	const ys = pts.map((p) => p.y);
	const x = Math.min(...xs) - MARGIN;
	const y = Math.min(...ys) - MARGIN;
	return {
		sectors,
		viewBox: { x, y, width: Math.max(...xs) + MARGIN - x, height: Math.max(...ys) + MARGIN - y }
	};
}
