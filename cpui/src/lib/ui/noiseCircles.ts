// Where the backdrop's circles are. A spring pulls each one along its own
// orbit, and the circles push each other apart, harder the closer they get, so
// they slide past one another: dense ember over dense sky adds up to white.
// Two centres may come as close as one radius, the larger of the pair's, and
// never closer. Everything is in the 1280px frame, centred on the screen.

const SPREAD = 1.6;
const SIZE = 0.8;
/** The spring, critically damped: slow enough to look like drifting. */
const PULL = 0.04;
const DAMP = 0.4;
/** How hard the circles push apart. */
const PUSH = 0.012;
/** The push stops growing here, as a share of the closest allowed distance. */
const MIN_GAP = 0.02;
/** Seconds per step of the simulation: it stays stable at any noise.speed. */
const STEP = 0.02;
const SETTLE_STEPS = 2000;
/** Repulsion starts at this many times the closest allowed distance. */
const REACH = 2;
/** Passes of the hard limit: moving a pair apart can push one into a third. */
const LIMIT_PASSES = 4;

/** `period` is seconds per orbit; the sign of `dir` is its direction. */
const ORBITS = [
	{ r: 430, orbit: 150, period: 97, dir: 1 },
	{ r: 350, orbit: 230, period: 143, dir: -1 },
	{ r: 300, orbit: 320, period: 211, dir: 1 },
	{ r: 260, orbit: 400, period: 167, dir: -1 }
];

export const CIRCLE_COUNT = ORBITS.length;

export function noiseCircles() {
	const circles = ORBITS.map((o) => ({
		r: o.r * SIZE,
		reach: o.orbit * SPREAD,
		turn: (o.dir * 2 * Math.PI) / o.period,
		from: Math.random() * 2 * Math.PI,
		x: 0,
		y: 0,
		vx: 0,
		vy: 0
	}));
	type Circle = (typeof circles)[number];
	const pairs: [Circle, Circle][] = circles.flatMap((a, i) =>
		circles.slice(i + 1).map((b): [Circle, Circle] => [a, b])
	);
	let clock = 0;

	const orbit = (c: Circle) => {
		const a = c.turn * clock + c.from;
		return {
			x: c.reach * (Math.cos(a) + 0.25 * Math.cos(2.3 * a)),
			y: c.reach * (0.55 * Math.sin(a) + 0.15 * Math.sin(1.7 * a))
		};
	};

	const apart = (a: Circle, b: Circle) => {
		const dx = b.x - a.x;
		const dy = b.y - a.y;
		// Exactly on top of each other has no direction: pick one.
		const d = Math.hypot(dx, dy) || 1e-6;
		return { nx: dx / d || 1, ny: dy / d, d, closest: Math.max(a.r, b.r) };
	};

	const step = (h: number) => {
		for (const c of circles) {
			const at = orbit(c);
			c.vx += h * (PULL * (at.x - c.x) - DAMP * c.vx);
			c.vy += h * (PULL * (at.y - c.y) - DAMP * c.vy);
		}
		for (const [a, b] of pairs) {
			const { nx, ny, d, closest } = apart(a, b);
			if (d >= REACH * closest) continue;
			const gap = Math.max(d / closest - 1, MIN_GAP);
			const push = h * PUSH * closest * ((REACH - 1) / gap - 1);
			a.vx -= nx * push;
			a.vy -= ny * push;
			b.vx += nx * push;
			b.vy += ny * push;
		}
		for (const c of circles) {
			c.x += h * c.vx;
			c.y += h * c.vy;
		}
		for (let pass = 0; pass < LIMIT_PASSES; pass++) {
			for (const [a, b] of pairs) {
				const { nx, ny, d, closest } = apart(a, b);
				if (d >= closest) continue;
				const half = (closest - d) / 2;
				a.x -= nx * half;
				a.y -= ny * half;
				b.x += nx * half;
				b.y += ny * half;
				// Stop them closing, or they lean on the limit.
				const closing = Math.min(0, (b.vx - a.vx) * nx + (b.vy - a.vy) * ny) / 2;
				a.vx += nx * closing;
				a.vy += ny * closing;
				b.vx -= nx * closing;
				b.vy -= ny * closing;
			}
		}
	};

	for (const c of circles) Object.assign(c, orbit(c));
	for (let i = 0; i < SETTLE_STEPS; i++) step(STEP);

	return {
		circles,
		/** Move on by `seconds` of orbit. */
		advance(seconds: number) {
			const steps = Math.ceil(seconds / STEP);
			for (let i = 0; i < steps; i++) {
				clock += seconds / steps;
				step(seconds / steps);
			}
		}
	};
}
