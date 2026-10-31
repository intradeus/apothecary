<script lang="ts">
	// The backdrop (DESIGN 2.2): four grainy circles orbiting the middle of the
	// screen, ember and sky in turn, keeping out of each other's way
	// (noiseCircles). One fragment shader on one canvas computes every pixel
	// from its device coordinates, so the grain stays one device pixel on any
	// screen. Every frame re-blurs all the glass above it, so it draws as little
	// as it can get away with: on a timer, not at all in a hidden tab, and only
	// for a colour change under reduced motion. Each theme colours it with
	// --nEmb, --nSky and --nBlend in tokens.css; noise.blue and noise.speed
	// drive it live. Without WebGL2 the canvas stays hidden and the backdrop is
	// plain --frame.
	import { onMount } from 'svelte';
	import { noise } from './noise.svelte';
	import { CIRCLE_COUNT, noiseCircles } from './noiseCircles';
	import { token } from './tokens';

	const REST_FPS = 10;
	/** While a turn is under way or the circles are hurrying, which steps show. */
	const BUSY_FPS = 20;
	/** How long noise.blue takes to reach a new value. */
	const TURN_MS = 3000;

	const VERTEX = `#version 300 es
void main(){vec2 p=vec2((gl_VertexID<<1)&2,gl_VertexID&2);gl_Position=vec4(p*2.-1.,0,1);}`;

	const FRAGMENT = `#version 300 es
precision highp float;
out vec4 o;
uniform vec3 blob[${CIRCLE_COUNT}]; // centre xy, radius: device px, y up
uniform vec3 hue[2];                  // ember, sky
uniform vec3 frame;
uniform float shift;                  // -1 all ember, 0 each its own, 1 all sky
uniform float grain;                  // the floor grain; darkens on a light theme
uniform bool light;
const float DENSEST=.72, LIT=.85;
float h(vec2 p){vec3 q=fract(vec3(p.xyx)*.1031);q+=dot(q,q.yzx+33.33);return fract((q.x+q.y)*q.z);}
void main(){
  vec2 g=floor(gl_FragCoord.xy);
  float v=LIT*mix(.55,1.,h(g+5.));     // not every dot equally bright
  vec3 c=frame;
  for(int i=0;i<${CIRCLE_COUNT};i++){
    float k=float(i)*41.;
    float l=length(gl_FragCoord.xy-blob[i].xy)/blob[i].z;
    if(h(g+k)>=DENSEST*exp(-3.*l*l))continue; // each circle has its own dots
    int own=i%2;
    // Each dot changes colour at its own moment, so a turn never mixes the hues.
    vec3 t=hue[h(g+k+13.)<(own==0?shift:-shift)?1-own:own];
    c=light?c*(1.-v*(1.-t)):1.-(1.-c)*(1.-t*v);
  }
  o=vec4(c+grain*vec3(1.,.8,.7)*step(.8,h(g+9.)),1.);
}`;

	let canvas: HTMLCanvasElement;
	let live = $state(false);

	onMount(() => {
		const gl = canvas.getContext('webgl2', {
			alpha: false,
			antialias: false,
			depth: false,
			stencil: false,
			powerPreference: 'low-power'
		});
		if (!gl) return;

		const still = matchMedia('(prefers-reduced-motion: reduce)');
		let program: WebGLProgram | null = null;
		let raf = 0;
		let timer = 0;
		let last = performance.now();
		const { circles, advance } = noiseCircles();
		let mix = noise.blue;
		let turn = { from: mix, to: mix, at: 0 };
		let stale = true;

		const uniform = (name: string) => gl.getUniformLocation(program!, name);
		const rgb = (name: string) => {
			const n = parseInt(token(name).slice(1), 16);
			return [n >> 16, (n >> 8) & 255, n & 255].map((v) => v / 255);
		};

		const theme = () => {
			if (!program) return;
			const light = token('--nBlend') === 'multiply';
			gl.uniform3fv(uniform('hue'), [...rgb('--nEmb'), ...rgb('--nSky')]);
			gl.uniform3fv(uniform('frame'), rgb('--frame'));
			gl.uniform1f(uniform('grain'), (light ? -0.04 : 0.04) * Number(token('--grainA')));
			gl.uniform1i(uniform('light'), light ? 1 : 0);
			stale = true;
		};

		const draw = () => {
			const { width: W, height: H } = canvas;
			const u = Math.min(W, (H * 16) / 9) / 1280;
			gl.uniform3fv(
				uniform('blob'),
				circles.flatMap((c) => [W / 2 + c.x * u, H / 2 + c.y * u, c.r * u])
			);
			gl.uniform1f(uniform('shift'), (mix - 50) / 50);
			gl.drawArrays(gl.TRIANGLES, 0, 3);
			live = true;
		};

		// The exact device pixels the browser lays the canvas on. Worked out from
		// innerWidth, a fractional zoom leaves the buffer a fraction of a pixel
		// off its box, and the stretch blurs the grain.
		const fit = new ResizeObserver(([entry]) => {
			const box = entry.devicePixelContentBoxSize?.[0];
			const w = box?.inlineSize ?? Math.round(entry.contentRect.width * devicePixelRatio);
			const h = box?.blockSize ?? Math.round(entry.contentRect.height * devicePixelRatio);
			if (canvas.width === w && canvas.height === h) return;
			canvas.width = w;
			canvas.height = h;
			gl.viewport(0, 0, w, h);
			// Resizing clears the buffer: paint now, or it flashes until the next tick.
			if (program) draw();
		});

		const tick = (now: number) => {
			// Capped so an orbit doesn't leap when a hidden tab comes back.
			const dt = Math.min(now - last, 250) / 1000;
			last = now;

			if (noise.blue !== turn.to) turn = { from: mix, to: noise.blue, at: now };
			const turning = mix !== turn.to;
			if (turning) {
				const p = Math.min(1, (now - turn.at) / TURN_MS);
				mix = turn.from + (turn.to - turn.from) * p * p * (3 - 2 * p);
			}
			const moving = !still.matches && noise.speed > 0;
			if (moving) advance(dt * noise.speed);

			if (moving || turning || stale) {
				stale = false;
				draw();
			}
			// A timer, not a requestAnimationFrame that skips frames: that wakes
			// the page at the display's rate just to do nothing. The frame request
			// is what stops it in a hidden tab.
			const fps = turning || noise.speed > 1 ? BUSY_FPS : REST_FPS;
			timer = window.setTimeout(() => (raf = requestAnimationFrame(tick)), 1000 / fps);
		};

		const start = () => {
			const shader = (type: number, source: string) => {
				const s = gl.createShader(type)!;
				gl.shaderSource(s, source);
				gl.compileShader(s);
				return s;
			};
			const p = gl.createProgram()!;
			gl.attachShader(p, shader(gl.VERTEX_SHADER, VERTEX));
			gl.attachShader(p, shader(gl.FRAGMENT_SHADER, FRAGMENT));
			gl.linkProgram(p);
			if (!gl.getProgramParameter(p, gl.LINK_STATUS)) return;
			program = p;
			gl.useProgram(p);
			gl.bindVertexArray(gl.createVertexArray());
			gl.viewport(0, 0, canvas.width, canvas.height);
			theme();
			raf = requestAnimationFrame(tick);
		};

		const stop = () => {
			clearTimeout(timer);
			cancelAnimationFrame(raf);
		};

		const lost = (e: Event) => {
			e.preventDefault();
			stop();
			program = null;
			live = false;
		};

		const themes = new MutationObserver(theme);
		themes.observe(document.documentElement, {
			attributes: true,
			attributeFilter: ['data-theme']
		});
		canvas.addEventListener('webglcontextlost', lost);
		canvas.addEventListener('webglcontextrestored', start);
		try {
			fit.observe(canvas, { box: 'device-pixel-content-box' });
		} catch {
			// Safari has no such box.
			fit.observe(canvas);
		}
		start();

		return () => {
			stop();
			fit.disconnect();
			themes.disconnect();
			canvas.removeEventListener('webglcontextlost', lost);
			canvas.removeEventListener('webglcontextrestored', start);
		};
	});
</script>

<div class="noise" aria-hidden="true">
	<canvas bind:this={canvas} class:live></canvas>
</div>

<style>
	.noise {
		position: fixed;
		inset: 0;
		z-index: var(--z-scene);
		overflow: hidden;
		pointer-events: none;
		background: var(--frame);
	}
	canvas {
		display: block;
		width: 100%;
		height: 100%;
		image-rendering: pixelated;
		visibility: hidden;
	}
	canvas.live {
		visibility: visible;
	}
</style>
