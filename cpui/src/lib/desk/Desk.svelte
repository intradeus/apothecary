<script lang="ts" module>
	import type { ViewId } from './tree';

	/** What a view shows in its tab and its leaf's strip. */
	export interface ViewMeta {
		count?: string | number;
		/** A count that wants attention: amber = a human must act, rose = blocked. */
		alert?: 'amb' | 'ros';
		hint?: string;
	}

	/** What a view knows about where it is drawn. */
	export interface ViewPlace {
		/** Drawn right now (its leaf is on screen and it is the active tab). */
		visible: boolean;
		w: number;
		h: number;
	}
</script>

<script lang="ts">
	// The tiling desk (DESIGN §6). Layout is computed — absolute rects from the
	// tree — and every view is rendered ONCE in its own absolutely positioned
	// host whose rect follows its leaf. Moving a tab between leaves only moves
	// the host, so a terminal session or a stream survives a re-dock. A view
	// mounts the first time it is shown and unmounts when hidden.
	//
	// Feel (DESIGN 2.5):
	//  - a tab drag commits after a few px, then the ghost tracks the pointer
	//    1:1 from where the tab was grabbed (pointer capture); the drop
	//    preview springs between zones; a drop springs the windows into their
	//    new rects, a release over nothing flies the ghost home
	//  - a gutter tracks 1:1 from the grab point, rubber-bands past the
	//    smallest pane size and bounces back to the limit on release
	//  - split, hide, preset, maximise and restore spring every window from
	//    its live rect; windows that appear materialise, ones that go fade
	import { onMount, type Snippet } from 'svelte';
	import type { TransitionConfig } from 'svelte/transition';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/ui/icons';
	import { ms, rubberband } from '$lib/ui/motion';
	import { token } from '$lib/ui/tokens';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import LeafWindow from './LeafWindow.svelte';
	import type { DeskState } from './desk.svelte';
	import {
		VIEW_LABEL,
		VIEWS,
		getAt,
		hit,
		lay,
		leavesOf,
		setAt,
		type Divider,
		type Geo,
		type LeafRect,
		type Tree
	} from './tree';

	let {
		desk,
		meta,
		view
	}: {
		desk: DeskState;
		meta: (v: ViewId) => ViewMeta;
		view: Snippet<[ViewId, ViewPlace]>;
	} = $props();

	let el = $state<HTMLDivElement | null>(null);
	let W = $state(0);
	let H = $state(0);
	let resizing = $state<'' | 'col-resize' | 'row-resize'>('');
	/** The gutter being dragged (its grip lights up on pointer-down). */
	let held = $state('');

	// Geometry comes from tokens.css (--desk-gap, --leaf-head, --pane-min).
	let geo = $state<Geo | null>(null);
	const px = (name: string) => parseFloat(token(name)) || 0;
	onMount(() => {
		geo = { gap: px('--desk-gap'), head: px('--leaf-head'), min: px('--pane-min') };
	});

	const layout = $derived.by(() => {
		const out: { leaves: LeafRect[]; divs: Divider[] } = { leaves: [], divs: [] };
		const t = desk.tree;
		if (!t || !W || !H || !geo) return out;
		const maxLeaf = desk.max ? leavesOf(t).find((l) => l.id === desk.max) : undefined;
		if (maxLeaf) out.leaves.push({ n: maxLeaf, x: 0, y: 0, w: W, h: H });
		else lay(t, 0, 0, W, H, [], out, geo.gap);
		return out;
	});
	$effect(() => {
		desk.rects = layout.leaves;
	});

	// Windows enter and leave with motion once the desk has painted once:
	// the first layout just appears.
	let appear = $state(false);
	$effect(() => {
		if (appear || !layout.leaves.length) return;
		const f = requestAnimationFrame(() => (appear = true));
		return () => cancelAnimationFrame(f);
	});

	type Rect = { x: number; y: number; w: number; h: number };
	// Where each view is drawn: the body of its leaf when it is that leaf's
	// active tab and the leaf is laid out; nowhere otherwise.
	const places = $derived.by(() => {
		const m = new Map<ViewId, Rect & { leaf: string }>();
		const head = geo?.head ?? 0;
		for (const r of layout.leaves)
			m.set(r.n.act, {
				x: r.x + 1,
				y: r.y + head,
				w: Math.max(0, r.w - 2),
				h: Math.max(0, r.h - head - 1),
				leaf: r.n.id
			});
		return m;
	});

	// Mounted views: shown at least once and still somewhere in the tree.
	const ever = new Set<ViewId>();
	const mounted = $derived.by(() => {
		for (const v of places.keys()) ever.add(v);
		for (const v of [...ever]) if (!desk.visible.has(v)) ever.delete(v);
		return new Set(ever);
	});

	// Each mounted view's host: drawn ('on'), an inactive tab of a window on
	// screen ('tab', not drawn at all), or in a window that is off screen
	// because another one is maximised ('away': it fades out where it was,
	// and comes back from there on restore).
	const lastRect = new Map<ViewId, Rect>();
	const lastLeaf = new Map<ViewId, string>();
	const laidIds = $derived(new Set(layout.leaves.map((r) => r.n.id)));
	const hosts = $derived.by(() => {
		const m = new Map<ViewId, { r: Rect | undefined; state: 'on' | 'tab' | 'away' }>();
		for (const v of mounted) {
			const p = places.get(v);
			if (p) {
				lastRect.set(v, p);
				lastLeaf.set(v, p.leaf);
				m.set(v, { r: p, state: 'on' });
				continue;
			}
			const l = desk.leafOf(v);
			if (l) lastLeaf.set(v, l.id);
			m.set(v, {
				r: lastRect.get(v),
				state: l && laidIds.has(l.id) ? 'tab' : lastRect.has(v) ? 'away' : 'tab'
			});
		}
		return m;
	});

	/** A view leaving with its window fades with it; one closed as a tab of
	 *  a window that stays goes at once (the next tab is already there). */
	function hostOut(_node: Element, v: ViewId): TransitionConfig {
		const l = lastLeaf.get(v);
		if (!appear || (l && laidIds.has(l))) return { duration: 0 };
		return { duration: ms('--dur-fade', 160), css: (t) => `opacity:${t}` };
	}
	function fadeIn(_node: Element): TransitionConfig {
		return { duration: ms('--dur-fade', 160), css: (t) => `opacity:${t}` };
	}

	const leafIdOf = (v: ViewId) => desk.leafOf(v)?.id ?? null;

	function deskPoint(e: { clientX: number; clientY: number }) {
		const b = el!.getBoundingClientRect();
		return { x: e.clientX - b.left, y: e.clientY - b.top };
	}

	// ── tab drag ──────────────────────────────────────────────────────────

	/** Pointer travel before a press on a tab becomes a drag. */
	const INTENT = 6;

	function tabDown(leafId: string, v: ViewId, e: PointerEvent) {
		if (e.button !== 0 || !el) return;
		e.preventDefault();
		e.stopPropagation();
		const tab = e.currentTarget as HTMLElement;
		const r = tab.getBoundingClientRect();
		const b = el.getBoundingClientRect();
		// Where on the tab it was grabbed: the ghost keeps that offset.
		const ox = e.clientX - r.left;
		const oy = e.clientY - r.top;
		const home = { x: r.left - b.left, y: r.top - b.top };
		const x0 = e.clientX;
		const y0 = e.clientY;
		const id = e.pointerId;
		let dragging = false;
		desk.focus = leafId;
		desk.menuOpen = false;
		tab.setPointerCapture(id);

		const move = (ev: PointerEvent) => {
			if (ev.pointerId !== id) return;
			if (!dragging) {
				if (Math.hypot(ev.clientX - x0, ev.clientY - y0) < INTENT) return;
				dragging = true;
			}
			const p = deskPoint(ev);
			desk.drag = { v, x: p.x, y: p.y, ox, oy, home };
			desk.drop = geo ? hit(desk.rects, p, geo.head) : null;
		};
		const end = (ev: PointerEvent | null) => {
			if (ev && ev.pointerId !== id) return;
			tab.removeEventListener('pointermove', move);
			tab.removeEventListener('pointerup', end);
			tab.removeEventListener('pointercancel', cancel);
			window.removeEventListener('keydown', key);
			if (tab.hasPointerCapture(id)) tab.releasePointerCapture(id);
			if (!dragging) {
				if (ev?.type === 'pointerup') desk.activate(leafId, v);
				return;
			}
			const d = ev?.type === 'pointerup' ? desk.drop : null;
			desk.drop = null;
			if (d) {
				desk.drag = null;
				desk.moveView(v, leafId, d);
			} else flyHome();
		};
		const cancel = (ev: PointerEvent) => end(ev.pointerId === id ? null : ev);
		const key = (ev: KeyboardEvent) => ev.key === 'Escape' && end(null);
		tab.addEventListener('pointermove', move);
		tab.addEventListener('pointerup', end);
		tab.addEventListener('pointercancel', cancel);
		window.addEventListener('keydown', key);
	}

	// Released over nothing (or Esc): the ghost flies back to its tab on the
	// bounce, then lets go.
	let homing: ReturnType<typeof setTimeout> | undefined;
	function flyHome() {
		const d = desk.drag;
		if (!d) return;
		desk.drag = { ...d, x: d.home.x + d.ox, y: d.home.y + d.oy, back: true };
		clearTimeout(homing);
		homing = setTimeout(
			() => {
				if (desk.drag?.back) desk.drag = null;
			},
			ms('--dur-bounce', 600)
		);
	}

	// ── gutter drag ───────────────────────────────────────────────────────

	const gutterKey = (d: Divider) => d.path.join('.') + ':' + d.i;

	function withSize(t: Tree, d: Divider, sum: number, aPx: number): Tree {
		if (!t) return t;
		const a = aPx / d.tot;
		return setAt(t, d.path, (n) =>
			n.k === 'split'
				? { ...n, s: n.s.map((v, k) => (k === d.i ? a : k === d.i + 1 ? sum - a : v)) }
				: n
		);
	}

	// Resize the two panes either side, 1:1 from the grab point. Past the
	// smallest pane size it resists (rubber band) rather than stopping dead,
	// and on release springs back to the limit.
	function divDown(d: Divider, e: PointerEvent) {
		if (e.button !== 0 || !geo) return;
		e.preventDefault();
		e.stopPropagation();
		const t0 = desk.tree;
		if (!t0) return;
		const node = getAt(t0, d.path);
		if (node.k !== 'split') return;
		const g = e.currentTarget as HTMLElement;
		const id = e.pointerId;
		g.setPointerCapture(id);
		held = gutterKey(d);
		resizing = d.row ? 'col-resize' : 'row-resize';

		const sum = node.s[d.i] + node.s[d.i + 1];
		const sumPx = sum * d.tot;
		const a0 = node.s[d.i] * d.tot;
		// Never jump on grab: a pane already under the minimum keeps its size
		// as its own limit.
		const lo = Math.min(geo.min, a0, sumPx / 2);
		const hi = Math.max(sumPx - geo.min, a0, sumPx / 2);
		const band = geo.min / 2;
		const p0 = d.row ? e.clientX : e.clientY;
		let raw = a0;

		const move = (ev: PointerEvent) => {
			if (ev.pointerId !== id) return;
			raw = a0 + ((d.row ? ev.clientX : ev.clientY) - p0);
			let a = raw;
			if (a < lo) a = lo - rubberband(lo - a, band);
			else if (a > hi) a = hi + rubberband(a - hi, band);
			desk.resizeLive(withSize(desk.tree, d, sum, Math.max(0, Math.min(sumPx, a))));
		};
		const up = (ev: PointerEvent) => {
			if (ev.pointerId !== id) return;
			g.removeEventListener('pointermove', move);
			g.removeEventListener('pointerup', up);
			g.removeEventListener('pointercancel', up);
			held = '';
			resizing = '';
			const a = Math.max(lo, Math.min(hi, raw));
			desk.resizeDone(withSize(desk.tree, d, sum, a), a !== raw);
		};
		g.addEventListener('pointermove', move);
		g.addEventListener('pointerup', up);
		g.addEventListener('pointercancel', up);
	}

	$effect(() => {
		const c = resizing || (desk.drag && !desk.drag.back ? 'grabbing' : '');
		document.body.style.cursor = c;
		return () => (document.body.style.cursor = '');
	});
</script>

<div
	class="desk"
	bind:this={el}
	bind:clientWidth={W}
	bind:clientHeight={H}
	class:busy={!!resizing || !!desk.drag}
>
	{#each layout.leaves as r (r.n.id)}
		<LeafWindow
			leaf={r.n}
			x={r.x}
			y={r.y}
			w={r.w}
			h={r.h}
			focused={desk.focus === r.n.id}
			maxed={desk.max === r.n.id}
			dragging={desk.drag?.v ?? null}
			motion={desk.motion}
			{appear}
			{meta}
			ontabdown={(v, e) => tabDown(r.n.id, v, e)}
			onactivate={(v) => desk.activate(r.n.id, v)}
			onfocus={() => desk.focus !== r.n.id && (desk.focus = r.n.id)}
			onsplit={(zone) => desk.moveView(r.n.act, r.n.id, { id: r.n.id, zone })}
			onmax={() => desk.toggleMax(r.n.id)}
			onclose={() => desk.closeLeaf(r.n.id)}
			onclosetab={(v) => desk.closeView(v)}
			compact={desk.compact}
		/>
	{/each}

	{#each layout.divs as d (gutterKey(d))}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="gutter"
			class:row={d.row}
			class:held={held === gutterKey(d)}
			style:left="{Math.round(d.x)}px"
			style:top="{Math.round(d.y)}px"
			style:width="{Math.round(d.w)}px"
			style:height="{Math.round(d.h)}px"
			onpointerdown={(e) => divDown(d, e)}
		>
			<span class="grip"></span>
		</div>
	{/each}

	{#each VIEWS as v (v)}
		{#if mounted.has(v)}
			{@const hs = hosts.get(v)}
			{@const p = hs?.r}
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div
				class="host"
				class:tab={!p || hs?.state === 'tab'}
				class:away={hs?.state === 'away'}
				class:spring={desk.motion === 'spring'}
				class:bounce={desk.motion === 'bounce'}
				style:left="{Math.round(p?.x ?? 0)}px"
				style:top="{Math.round(p?.y ?? 0)}px"
				style:width="{Math.round(p?.w ?? 0)}px"
				style:height="{Math.round(p?.h ?? 0)}px"
				onpointerdown={() => {
					const id = places.get(v)?.leaf ?? leafIdOf(v);
					if (id && desk.focus !== id) desk.focus = id;
				}}
				out:hostOut={v}
			>
				{@render view(v, {
					visible: hs?.state === 'on',
					w: places.get(v)?.w ?? 0,
					h: places.get(v)?.h ?? 0
				})}
			</div>
		{/if}
	{/each}

	{#if desk.drop}
		<div
			class="drop serif"
			style:left="{Math.round(desk.drop.x)}px"
			style:top="{Math.round(desk.drop.y)}px"
			style:width="{Math.round(desk.drop.w)}px"
			style:height="{Math.round(desk.drop.h)}px"
			transition:fadeIn
		>
			{desk.drop.label}
		</div>
	{/if}
	{#if desk.drag}
		<div
			class="ghost"
			class:back={desk.drag.back}
			style:left="{Math.round(desk.drag.x - desk.drag.ox)}px"
			style:top="{Math.round(desk.drag.y - desk.drag.oy)}px"
			out:fadeIn
		>
			{VIEW_LABEL[desk.drag.v]}
		</div>
	{/if}

	{#if !desk.tree}
		<div class="bench" in:fadeIn>
			<EmptyState title={t('desk.empty.title')} text={t('desk.empty.text')}>
				<button
					class="btn-primary"
					aria-expanded={desk.menuOpen}
					onpointerdown={(e) => e.stopPropagation()}
					onclick={() => (desk.menuOpen = !desk.menuOpen)}>{t('desk.empty.open')} {I.menu}</button
				>
			</EmptyState>
		</div>
	{/if}
</div>

<style>
	.desk {
		flex: 1;
		min-height: 0;
		position: relative;
		/* The desk's own stacking: hosts over windows, gutters over hosts,
		 * the drag preview and ghost over everything here. */
		isolation: isolate;
		margin: 10px 16px 12px;
	}
	.desk.busy {
		user-select: none;
	}
	.desk.busy .host {
		pointer-events: none;
	}
	.host {
		position: absolute;
		z-index: var(--z-raise);
		display: flex;
		min-height: 0;
		min-width: 0;
		flex-direction: column;
		overflow: hidden;
		border-radius: 0 0 calc(var(--r-pane) - 1px) calc(var(--r-pane) - 1px);
		/* Views size their own layout from the window they are in. */
		container-type: size;
		transform-origin: center;
		transition:
			opacity var(--dur-fade) var(--ease-out),
			transform var(--dur-spring) var(--spring);
	}
	/* An inactive tab: not drawn at all (a tab switch is instant). */
	.host.tab {
		display: none;
	}
	/* Off screen behind a maximised window: fades and settles where it was. */
	.host.away {
		opacity: 0;
		transform: scale(var(--enter-scale));
		visibility: hidden;
		pointer-events: none;
		transition:
			opacity var(--dur-fade) var(--ease-out),
			transform var(--dur-spring) var(--spring),
			visibility 0s linear var(--dur-fade);
	}
	/* The rect follows its window's spring. */
	.host.spring {
		transition:
			left var(--dur-spring) var(--spring),
			top var(--dur-spring) var(--spring),
			width var(--dur-spring) var(--spring),
			height var(--dur-spring) var(--spring),
			opacity var(--dur-fade) var(--ease-out),
			transform var(--dur-spring) var(--spring);
	}
	.host.away.spring {
		transition:
			opacity var(--dur-fade) var(--ease-out),
			transform var(--dur-spring) var(--spring),
			visibility 0s linear var(--dur-fade);
	}
	.host.bounce {
		transition:
			left var(--dur-bounce) var(--spring-bounce),
			top var(--dur-bounce) var(--spring-bounce),
			width var(--dur-bounce) var(--spring-bounce),
			height var(--dur-bounce) var(--spring-bounce);
	}
	.gutter {
		position: absolute;
		z-index: calc(var(--z-raise) + 1);
		cursor: row-resize;
		touch-action: none;
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: var(--r-xs);
	}
	.gutter.row {
		cursor: col-resize;
	}
	.gutter:hover,
	.gutter.held {
		background: var(--brandBg);
	}
	.grip {
		width: 32px;
		height: 2px;
		border-radius: var(--r-xs);
		background: var(--ln2);
		opacity: 0;
	}
	.gutter.row .grip {
		width: 2px;
		height: 32px;
	}
	.gutter:hover .grip,
	.gutter.held .grip {
		opacity: 1;
	}
	.gutter.held .grip {
		background: var(--brand);
	}
	/* The drop preview springs between zones as the pointer moves. */
	.drop {
		position: absolute;
		z-index: var(--z-float);
		pointer-events: none;
		border-radius: var(--r-pane);
		background: var(--brandBg);
		border: 1.5px dashed var(--brand);
		box-shadow: 0 0 30px var(--brandLn);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: var(--fs-section);
		color: var(--brand);
		transition:
			left var(--dur-spring) var(--spring),
			top var(--dur-spring) var(--spring),
			width var(--dur-spring) var(--spring),
			height var(--dur-spring) var(--spring);
	}
	/* The dragged tab, lifted: it tracks the pointer 1:1 (no transition)
	 * until it is released over nothing and flies home on the bounce. */
	.ghost {
		position: absolute;
		z-index: calc(var(--z-float) + 1);
		pointer-events: none;
		padding: 3px 10px;
		border-radius: var(--r-tab);
		background: var(--sel);
		border: 1px solid var(--selLn);
		box-shadow: var(--dragSh);
		font-size: var(--fs-ui);
		font-weight: 600;
		color: var(--tx);
		white-space: nowrap;
		/* Lifted a hair (the inverse of a press); 1 under reduced motion. */
		transform: scale(calc(2 - var(--press-scale)));
		animation: ghost-lift var(--dur-spring) var(--spring);
	}
	.ghost.back {
		transform: scale(1);
		box-shadow: var(--selSh);
		transition:
			left var(--dur-bounce) var(--spring-bounce),
			top var(--dur-bounce) var(--spring-bounce),
			transform var(--dur-spring) var(--spring),
			box-shadow var(--dur-spring) var(--spring);
	}
	@keyframes ghost-lift {
		from {
			transform: scale(1);
			box-shadow: var(--selSh);
		}
	}
	.bench {
		position: absolute;
		inset: 0;
		display: flex;
		flex-direction: column;
	}
	.bench > :global(*) {
		flex: 1;
		justify-content: center;
	}
</style>
