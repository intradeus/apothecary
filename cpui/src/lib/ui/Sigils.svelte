<script lang="ts">
	// The backdrop (DESIGN 2.2 "Sigils"): two slowly turning alchemical charts
	// in faint gold behind everything. ~20 fps, paused while the tab is hidden,
	// one still frame under prefers-reduced-motion.
	import { onMount } from 'svelte';
	import { prefs } from '$lib/prefs.svelte';

	let canvas = $state<HTMLCanvasElement | null>(null);

	const GLYPHS = ['☉', '☽', '☿', '♀', '♂', '♃', '♄'];

	function draw(ctx: CanvasRenderingContext2D, W: number, H: number, t: number, dark: boolean) {
		ctx.clearRect(0, 0, W, H);
		const gold = dark ? '233,205,130' : '120,90,30';
		// Charts are laid out for 1440×900 and scale with the viewport's
		// smaller relative dimension so they never crowd a small screen.
		const s = Math.min(W / 1440, H / 900);
		const chart = (cx: number, cy: number, R: number, rot: number, al: number) => {
			ctx.save();
			ctx.translate(cx, cy);
			ctx.strokeStyle = `rgba(${gold},${al})`;
			ctx.lineWidth = 1;
			for (const f of [1, 0.86, 0.62, 0.34]) {
				ctx.beginPath();
				ctx.arc(0, 0, R * f, 0, Math.PI * 2);
				ctx.stroke();
			}
			ctx.save();
			ctx.rotate(rot);
			for (let i = 0; i < 120; i++) {
				const a = (i / 120) * Math.PI * 2;
				const l = i % 10 === 0 ? 14 : i % 5 === 0 ? 9 : 5;
				ctx.beginPath();
				ctx.moveTo(Math.cos(a) * R, Math.sin(a) * R);
				ctx.lineTo(Math.cos(a) * (R - l), Math.sin(a) * (R - l));
				ctx.stroke();
			}
			ctx.fillStyle = `rgba(${gold},${Math.min(1, al * 1.8)})`;
			ctx.font = `${Math.round(R * 0.075)}px "Instrument Serif", serif`;
			ctx.textAlign = 'center';
			ctx.textBaseline = 'middle';
			GLYPHS.forEach((g, i) => {
				const a = (i / GLYPHS.length) * Math.PI * 2 - Math.PI / 2;
				ctx.fillText(g + '︎', Math.cos(a) * R * 0.93, Math.sin(a) * R * 0.93);
			});
			ctx.restore();
			ctx.save();
			ctx.rotate(-rot * 1.6);
			const poly = (n: number, r: number, off: number) => {
				ctx.beginPath();
				for (let i = 0; i <= n; i++) {
					const a = off + (i / n) * Math.PI * 2;
					const x = Math.cos(a) * r;
					const y = Math.sin(a) * r;
					if (i) ctx.lineTo(x, y);
					else ctx.moveTo(x, y);
				}
				ctx.stroke();
			};
			poly(3, R * 0.86, -Math.PI / 2);
			poly(3, R * 0.86, Math.PI / 2);
			poly(4, R * 0.62, Math.PI / 4);
			ctx.restore();
			ctx.setLineDash([2, 6]);
			ctx.beginPath();
			ctx.moveTo(-R * 1.1, 0);
			ctx.lineTo(R * 1.1, 0);
			ctx.moveTo(0, -R * 1.1);
			ctx.lineTo(0, R * 1.1);
			ctx.stroke();
			ctx.setLineDash([]);
			ctx.restore();
		};
		chart(W * 0.66, H * 0.56, 420 * s, t * 0.012, dark ? 0.14 : 0.22);
		chart(W * 0.13, H * 0.2, 170 * s, -t * 0.02, dark ? 0.11 : 0.18);
		const g = ctx.createRadialGradient(W * 0.66, H * 0.56, 10, W * 0.66, H * 0.56, 320 * s);
		g.addColorStop(0, dark ? 'rgba(233,205,130,.08)' : 'rgba(233,205,130,.2)');
		g.addColorStop(1, 'rgba(0,0,0,0)');
		ctx.fillStyle = g;
		ctx.fillRect(0, 0, W, H);
	}

	onMount(() => {
		const c = canvas!;
		const ctx = c.getContext('2d')!;
		const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
		let W = 0;
		let H = 0;
		const resize = () => {
			const dpr = Math.min(2, window.devicePixelRatio || 1);
			W = window.innerWidth;
			H = window.innerHeight;
			c.width = Math.round(W * dpr);
			c.height = Math.round(H * dpr);
			ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
		};
		resize();
		const t0 = performance.now();
		let last = -1;
		let raf = 0;
		const frame = (now: number) => {
			const t = (now - t0) / 1000;
			if (t - last >= 1 / 20) {
				last = t;
				draw(ctx, W, H, t, prefs.theme !== 'light');
			}
			if (!reduce && !document.hidden) raf = requestAnimationFrame(frame);
		};
		const redraw = () => draw(ctx, W, H, (performance.now() - t0) / 1000, prefs.theme !== 'light');
		const onVis = () => {
			cancelAnimationFrame(raf);
			if (!document.hidden && !reduce) raf = requestAnimationFrame(frame);
		};
		const onResize = () => {
			resize();
			redraw();
		};
		raf = requestAnimationFrame(frame);
		document.addEventListener('visibilitychange', onVis);
		window.addEventListener('resize', onResize);
		// Re-tint on theme change even when motion is reduced.
		const stopTheme = $effect.root(() => {
			$effect(() => {
				void prefs.theme;
				redraw();
			});
		});
		return () => {
			cancelAnimationFrame(raf);
			document.removeEventListener('visibilitychange', onVis);
			window.removeEventListener('resize', onResize);
			stopTheme();
		};
	});
</script>

<canvas bind:this={canvas} class="sigils" aria-hidden="true"></canvas>

<style>
	.sigils {
		position: fixed;
		inset: 0;
		width: 100vw;
		height: 100vh;
		z-index: 0;
		pointer-events: none;
	}
</style>
