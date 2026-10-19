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
	import type { Snippet } from 'svelte';
	import { lore } from '$lib/lore';
	import LeafWindow from './LeafWindow.svelte';
	import type { DeskState } from './desk.svelte';
	import {
		HEAD,
		VIEW_LABEL,
		VIEWS,
		getAt,
		hit,
		lay,
		leavesOf,
		setAt,
		type Divider,
		type LeafRect
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

	const layout = $derived.by(() => {
		const out: { leaves: LeafRect[]; divs: Divider[] } = { leaves: [], divs: [] };
		const t = desk.tree;
		if (!t || !W || !H) return out;
		const maxLeaf = desk.max ? leavesOf(t).find((l) => l.id === desk.max) : undefined;
		if (maxLeaf) out.leaves.push({ n: maxLeaf, x: 0, y: 0, w: W, h: H });
		else lay(t, 0, 0, W, H, [], out);
		return out;
	});
	$effect(() => {
		desk.rects = layout.leaves;
	});

	// Where each view is drawn: the body of its leaf when it is that leaf's
	// active tab and the leaf is laid out; nowhere otherwise.
	const places = $derived.by(() => {
		const m = new Map<ViewId, { x: number; y: number; w: number; h: number; leaf: string }>();
		for (const r of layout.leaves)
			m.set(r.n.act, {
				x: r.x + 1,
				y: r.y + HEAD,
				w: Math.max(0, r.w - 2),
				h: Math.max(0, r.h - HEAD - 1),
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

	const leafIdOf = (v: ViewId) => desk.leafOf(v)?.id ?? null;

	function deskPoint(e: MouseEvent) {
		const b = el!.getBoundingClientRect();
		return { x: e.clientX - b.left, y: e.clientY - b.top };
	}

	function tabDown(leafId: string, v: ViewId, e: MouseEvent) {
		if (e.button) return;
		e.preventDefault();
		e.stopPropagation();
		const x0 = e.clientX;
		const y0 = e.clientY;
		let dragging = false;
		desk.focus = leafId;
		desk.menuOpen = false;
		const mv = (ev: MouseEvent) => {
			if (!dragging && Math.hypot(ev.clientX - x0, ev.clientY - y0) < 5) return;
			dragging = true;
			const p = deskPoint(ev);
			desk.drag = { v, x: p.x, y: p.y };
			desk.drop = hit(desk.rects, p);
		};
		const up = () => {
			window.removeEventListener('mousemove', mv);
			window.removeEventListener('mouseup', up);
			if (dragging) {
				const d = desk.drop;
				desk.drag = null;
				desk.drop = null;
				if (d) desk.moveView(v, leafId, d);
			} else desk.activate(leafId, v);
		};
		window.addEventListener('mousemove', mv);
		window.addEventListener('mouseup', up);
	}

	// Gutter drag: resize the two panes either side, never below ~120px wide
	// or 70px tall.
	function divDown(d: Divider, e: MouseEvent) {
		e.preventDefault();
		e.stopPropagation();
		const t0 = desk.tree;
		if (!t0) return;
		const node = getAt(t0, d.path);
		if (node.k !== 'split') return;
		const s0 = node.s.slice();
		const p0 = d.row ? e.clientX : e.clientY;
		const min = Math.min((d.row ? 120 : 70) / d.tot, (s0[d.i] + s0[d.i + 1]) / 2);
		const sum = s0[d.i] + s0[d.i + 1];
		const mv = (ev: MouseEvent) => {
			const t = desk.tree;
			if (!t) return;
			const dp = ((d.row ? ev.clientX : ev.clientY) - p0) / d.tot;
			const a = Math.max(min, Math.min(sum - min, s0[d.i] + dp));
			desk.set(
				setAt(t, d.path, (n) =>
					n.k === 'split'
						? { ...n, s: n.s.map((v, k) => (k === d.i ? a : k === d.i + 1 ? sum - a : v)) }
						: n
				)
			);
		};
		const up = () => {
			window.removeEventListener('mousemove', mv);
			window.removeEventListener('mouseup', up);
			resizing = '';
		};
		window.addEventListener('mousemove', mv);
		window.addEventListener('mouseup', up);
		resizing = d.row ? 'col-resize' : 'row-resize';
	}

	$effect(() => {
		const c = resizing || (desk.drag ? 'grabbing' : '');
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
			{meta}
			ontabdown={(v, e) => tabDown(r.n.id, v, e)}
			onactivate={(v) => desk.activate(r.n.id, v)}
			onfocus={() => desk.focus !== r.n.id && (desk.focus = r.n.id)}
			onsplit={(zone) => desk.moveView(r.n.act, r.n.id, { id: r.n.id, zone })}
			onmax={() => desk.toggleMax(r.n.id)}
			onclose={() => desk.closeLeaf(r.n.id)}
			onclosetab={(v) => desk.closeView(v)}
		/>
	{/each}

	{#each layout.divs as d (d.path.join('.') + ':' + d.i)}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="gutter"
			class:row={d.row}
			style:left="{Math.round(d.x)}px"
			style:top="{Math.round(d.y)}px"
			style:width="{Math.round(d.w)}px"
			style:height="{Math.round(d.h)}px"
			onmousedown={(e) => divDown(d, e)}
		>
			<span class="grip"></span>
		</div>
	{/each}

	{#each VIEWS as v (v)}
		{#if mounted.has(v)}
			{@const p = places.get(v)}
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div
				class="host"
				style:display={p ? 'flex' : 'none'}
				style:left="{Math.round(p?.x ?? 0)}px"
				style:top="{Math.round(p?.y ?? 0)}px"
				style:width="{Math.round(p?.w ?? 0)}px"
				style:height="{Math.round(p?.h ?? 0)}px"
				onmousedown={() => {
					const id = p?.leaf ?? leafIdOf(v);
					if (id && desk.focus !== id) desk.focus = id;
				}}
			>
				{@render view(v, { visible: !!p, w: p?.w ?? 0, h: p?.h ?? 0 })}
			</div>
		{/if}
	{/each}

	{#if desk.drop}
		<div
			class="drop"
			style:left="{Math.round(desk.drop.x)}px"
			style:top="{Math.round(desk.drop.y)}px"
			style:width="{Math.round(desk.drop.w)}px"
			style:height="{Math.round(desk.drop.h)}px"
		>
			{desk.drop.label}
		</div>
	{/if}
	{#if desk.drag}
		<div
			class="ghost"
			style:left="{Math.round(desk.drag.x + 12)}px"
			style:top="{Math.round(desk.drag.y + 10)}px"
		>
			{VIEW_LABEL[desk.drag.v]}
		</div>
	{/if}

	{#if !desk.tree}
		<div class="bench">
			<span class="t">{lore('emptyDesk')}</span>
			<span class="x"
				>Every window is hidden. Bring one back from Windows, or start from a layout.</span
			>
			<button
				class="btn-primary"
				onmousedown={(e) => e.stopPropagation()}
				onclick={() => (desk.menuOpen = !desk.menuOpen)}>Open Windows ▾</button
			>
		</div>
	{/if}
</div>

<style>
	.desk {
		flex: 1;
		min-height: 0;
		position: relative;
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
		z-index: 2;
		min-height: 0;
		min-width: 0;
		flex-direction: column;
		overflow: hidden;
		border-radius: 0 0 15px 15px;
		/* Views size their own layout from the window they are in. */
		container-type: size;
	}
	.gutter {
		position: absolute;
		z-index: 6;
		cursor: row-resize;
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: 4px;
	}
	.gutter.row {
		cursor: col-resize;
	}
	.gutter:hover {
		background: var(--brandBg);
	}
	.grip {
		width: 32px;
		height: 2px;
		border-radius: 2px;
		background: var(--ln2);
		opacity: 0;
	}
	.gutter.row .grip {
		width: 2px;
		height: 32px;
	}
	.gutter:hover .grip {
		opacity: 1;
	}
	.drop {
		position: absolute;
		z-index: 20;
		pointer-events: none;
		border-radius: 16px;
		background: var(--brandBg);
		border: 1.5px dashed var(--brand);
		box-shadow: 0 0 30px var(--brandLn);
		display: flex;
		align-items: center;
		justify-content: center;
		font-family: var(--serif);
		font-style: italic;
		font-size: 18px;
		color: var(--brand);
	}
	.ghost {
		position: absolute;
		z-index: 21;
		pointer-events: none;
		padding: 4px 11px;
		border-radius: 9px;
		background: var(--sel);
		border: 1px solid var(--selLn);
		box-shadow: 0 10px 30px rgba(0, 0, 0, 0.45);
		font-size: 12.5px;
		font-weight: 600;
		color: var(--tx);
		white-space: nowrap;
	}
	.bench {
		position: absolute;
		inset: 0;
		border-radius: 16px;
		border: 1.5px dashed var(--ln2);
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 10px;
		text-align: center;
		padding: 16px;
	}
	.bench .t {
		font-family: var(--serif);
		font-style: italic;
		font-size: 26px;
	}
	.bench .x {
		color: var(--mut);
		max-width: 40ch;
	}
	.bench .btn-primary {
		padding: 6px 14px;
		border-radius: 10px;
		font-size: 12.5px;
	}
</style>
