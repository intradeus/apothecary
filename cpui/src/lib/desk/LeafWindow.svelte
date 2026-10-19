<script lang="ts">
	// One leaf of the desk (DESIGN §6, 4.3): content glass with a 38px tab
	// strip — draggable tabs with a count and a × that hides the view — a
	// hint, and the window controls (split right / down, maximise, hide).
	// The views themselves are drawn by the desk on top of the body area, so
	// they stay mounted while tabs move between leaves.
	import { VIEW_LABEL, type Leaf, type ViewId } from './tree';
	import type { ViewMeta } from './Desk.svelte';

	let {
		leaf,
		x,
		y,
		w,
		h,
		focused,
		maxed,
		dragging,
		meta,
		ontabdown,
		onactivate,
		onfocus,
		onsplit,
		onmax,
		onclose,
		onclosetab
	}: {
		leaf: Leaf;
		x: number;
		y: number;
		w: number;
		h: number;
		focused: boolean;
		maxed: boolean;
		/** The view being dragged, dimmed in its strip. */
		dragging: ViewId | null;
		meta: (v: ViewId) => ViewMeta;
		ontabdown: (v: ViewId, e: MouseEvent) => void;
		onactivate: (v: ViewId) => void;
		onfocus: () => void;
		onsplit: (zone: 'right' | 'bottom') => void;
		onmax: () => void;
		onclose: () => void;
		onclosetab: (v: ViewId) => void;
	} = $props();

	const multi = $derived(leaf.tabs.length > 1);
	const hint = $derived(w > 420 ? (meta(leaf.act).hint ?? '') : '');
	const stop = (e: Event) => e.stopPropagation();
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<section
	class="leaf c"
	class:focused
	style:left="{Math.round(x)}px"
	style:top="{Math.round(y)}px"
	style:width="{Math.round(w)}px"
	style:height="{Math.round(h)}px"
	onmousedown={onfocus}
>
	<div class="strip">
		<div class="tabrow" role="tablist">
			{#each leaf.tabs as v (v)}
				{@const m = meta(v)}
				<div
					class="tab"
					class:on={v === leaf.act}
					class:ghosted={dragging === v}
					role="tab"
					tabindex="0"
					aria-selected={v === leaf.act}
					title="Drag to move or split"
					onmousedown={(e) => ontabdown(v, e)}
					onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && onactivate(v)}
				>
					{VIEW_LABEL[v]}
					{#if m.count !== undefined && m.count !== '' && m.count !== 0}
						<span class="n {m.alert ?? ''}">{m.count}</span>
					{/if}
					<span
						class="x"
						role="button"
						tabindex="-1"
						title="Hide window"
						onmousedown={stop}
						onclick={(e) => {
							e.stopPropagation();
							onclosetab(v);
						}}
						onkeydown={(e) => e.key === 'Enter' && onclosetab(v)}>×</span
					>
				</div>
			{/each}
		</div>
		{#if hint}<span class="hint">{hint}</span>{/if}
		<div class="ctl">
			<button
				class="btn-icon"
				class:dim={!multi}
				title={multi ? 'Split right' : 'Split right (needs a second tab)'}
				onmousedown={stop}
				onclick={() => multi && onsplit('right')}><span class="ic r"><span></span></span></button
			>
			<button
				class="btn-icon"
				class:dim={!multi}
				title={multi ? 'Split down' : 'Split down (needs a second tab)'}
				onmousedown={stop}
				onclick={() => multi && onsplit('bottom')}><span class="ic d"><span></span></span></button
			>
			<button
				class="btn-icon"
				title={maxed ? 'Restore' : 'Maximise'}
				onmousedown={stop}
				onclick={onmax}>{maxed ? '⤡' : '⤢'}</button
			>
			<button class="btn-icon big" title="Hide this window" onmousedown={stop} onclick={onclose}
				>×</button
			>
		</div>
	</div>
</section>

<style>
	.leaf {
		position: absolute;
		display: flex;
		flex-direction: column;
		border-radius: 16px;
		overflow: hidden;
	}
	.leaf.focused {
		border-color: var(--brandLn);
	}
	.strip {
		flex: none;
		height: 38px;
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 0 5px 0 6px;
		border-bottom: 1px solid var(--cLn);
	}
	.tabrow {
		flex: 1 1 auto;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: 2px;
		overflow: hidden;
	}
	.tab {
		flex: none;
		cursor: grab;
		user-select: none;
		white-space: nowrap;
		padding: 3px 4px 3px 10px;
		border-radius: 9px;
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: 12.5px;
		font-weight: 500;
		color: var(--mut);
		outline-offset: -2px;
	}
	.tab:hover {
		color: var(--tx);
	}
	.tab.on {
		background: var(--sel);
		box-shadow:
			inset 0 0 0 1px var(--selLn),
			var(--selSh);
		color: var(--tx);
		font-weight: 600;
	}
	.tab.ghosted {
		opacity: 0.35;
	}
	.n {
		font-family: var(--mono);
		font-size: 10px;
		font-weight: 600;
		padding: 0 5px;
		border-radius: 8px;
		color: var(--fnt);
	}
	.n.amb {
		background: var(--amb);
		color: var(--ink);
	}
	.n.ros {
		background: var(--ros);
		color: var(--rosInk);
	}
	.x {
		width: 17px;
		height: 17px;
		border-radius: 5px;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 13px;
		line-height: 1;
		color: var(--fnt);
		cursor: pointer;
	}
	.x:hover {
		background: var(--p3);
		color: var(--tx);
	}
	.hint {
		/* Gives way to the tabs first. */
		flex: 0 1000 auto;
		font-size: 11px;
		color: var(--fnt);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
		max-width: 40%;
	}
	.ctl {
		flex: none;
		display: flex;
		gap: 1px;
	}
	.btn-icon.dim {
		opacity: 0.3;
		cursor: default;
	}
	.btn-icon.big {
		font-size: 15px;
	}
	.ic {
		width: 13px;
		height: 11px;
		border: 1.3px solid currentColor;
		border-radius: 3px;
		display: flex;
	}
	.ic.r {
		justify-content: flex-end;
	}
	.ic.r > span {
		width: 50%;
		border-left: 1.3px solid currentColor;
	}
	.ic.d {
		flex-direction: column;
		justify-content: flex-end;
	}
	.ic.d > span {
		height: 50%;
		border-top: 1.3px solid currentColor;
	}
</style>
