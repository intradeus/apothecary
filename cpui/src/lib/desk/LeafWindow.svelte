<script lang="ts">
	// One leaf of the desk (DESIGN §6, 4.3): content glass with a tab strip
	// (--leaf-head) — draggable tabs with a count and a × that hides the
	// view — a hint, and the window controls (split right / down, maximise,
	// hide). The views themselves are drawn by the desk on top of the body
	// area, so they stay mounted while tabs move between leaves.
	//
	// Motion: the rect springs to wherever the layout puts it (the desk turns
	// that on for discrete changes only); a new window materialises, a hidden
	// one scales and fades back out; the selected tab's bead slides.
	//
	// Compact (a phone): the only window. Its tabs are tapped, not dragged,
	// and the row scrolls sideways under a swipe, keeping the selected tab in
	// view; nothing to split, maximise or hide.
	import type { TransitionConfig } from 'svelte/transition';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/ui/icons';
	import { bump, indicator, ms, spring } from '$lib/ui/motion';
	import { keepInView } from '$lib/ui/inview';
	import { token } from '$lib/ui/tokens';
	import { VIEW_LABEL, type Leaf, type ViewId } from './tree';
	import type { ViewMeta } from './Desk.svelte';
	import type { DeskMotion } from './desk.svelte';

	let {
		leaf,
		x,
		y,
		w,
		h,
		focused,
		maxed,
		dragging,
		motion,
		appear,
		meta,
		ontabdown,
		onactivate,
		onfocus,
		onsplit,
		onmax,
		onclose,
		onclosetab,
		compact = false
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
		/** How the rect moves right now ('' = follows 1:1). */
		motion: DeskMotion;
		/** Enter and leave with motion (off for the desk's first paint). */
		appear: boolean;
		meta: (v: ViewId) => ViewMeta;
		ontabdown: (v: ViewId, e: PointerEvent) => void;
		onactivate: (v: ViewId) => void;
		onfocus: () => void;
		onsplit: (zone: 'right' | 'bottom') => void;
		onmax: () => void;
		onclose: () => void;
		onclosetab: (v: ViewId) => void;
		compact?: boolean;
	} = $props();

	const multi = $derived(leaf.tabs.length > 1);
	const hint = $derived(w > 420 && !compact ? (meta(leaf.act).hint ?? '') : '');
	const stop = (e: Event) => e.stopPropagation();
	const num = (c: ViewMeta['count']) => (typeof c === 'number' ? c : Number(c) || 0);

	/** A window arriving or leaving: scale and opacity together, from its
	 *  own centre, on the spring; reversible mid-flight. */
	function windowMotion(_node: Element, on: boolean): TransitionConfig {
		if (!on) return { duration: 0 };
		const move = ms('--dur-spring', 450);
		const fade = ms('--dur-fade', 160);
		const s0 = parseFloat(token('--enter-scale')) || 1;
		const duration = Math.max(move, fade);
		return {
			duration,
			css: (p) => {
				const m = spring(Math.min(1, (p * duration) / move));
				const o = Math.min(1, (p * duration) / fade);
				return `opacity:${o};transform:scale(${s0 + (1 - s0) * m})`;
			}
		};
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<section
	class="leaf c"
	class:compact
	class:focused
	class:spring={motion === 'spring'}
	class:bounce={motion === 'bounce'}
	style:left="{Math.round(x)}px"
	style:top="{Math.round(y)}px"
	style:width="{Math.round(w)}px"
	style:height="{Math.round(h)}px"
	onpointerdown={onfocus}
	transition:windowMotion={appear}
>
	<div class="strip">
		<div class="tabrow" role="tablist" use:indicator use:keepInView>
			{#each leaf.tabs as v (v)}
				{@const m = meta(v)}
				<div
					class="tab"
					class:on={v === leaf.act}
					class:ghosted={dragging === v}
					role="tab"
					tabindex="0"
					aria-selected={v === leaf.act}
					title={compact ? VIEW_LABEL[v] : t('desk.tab.drag')}
					onpointerdown={(e) => !compact && ontabdown(v, e)}
					onclick={() => compact && onactivate(v)}
					onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && onactivate(v)}
				>
					{VIEW_LABEL[v]}
					{#if m.count !== undefined && m.count !== '' && m.count !== 0}
						<span
							class="count"
							class:hot={m.alert === 'amb'}
							class:bad={m.alert === 'ros'}
							use:bump={num(m.count)}>{m.count}</span
						>
					{/if}
					{#if !compact}<span
							class="x"
							role="button"
							tabindex="-1"
							title={t('desk.tab.hide')}
							aria-label={t('desk.tab.hideNamed', { name: VIEW_LABEL[v] })}
							onpointerdown={stop}
							onclick={(e) => {
								e.stopPropagation();
								onclosetab(v);
							}}
							onkeydown={(e) => e.key === 'Enter' && onclosetab(v)}>{I.close}</span
						>{/if}
				</div>
			{/each}
		</div>
		{#if hint}<span class="hint tiny fnt ell">{hint}</span>{/if}
		{#if !compact}<div class="ctl">
				<button
					class="btn-icon"
					class:dim={!multi}
					title={multi ? t('desk.leaf.splitRight') : t('desk.leaf.splitRightNeeds')}
					aria-label={t('desk.leaf.splitRight')}
					onpointerdown={stop}
					onclick={() => multi && onsplit('right')}>{I.splitRight}</button
				>
				<button
					class="btn-icon"
					class:dim={!multi}
					title={multi ? t('desk.leaf.splitDown') : t('desk.leaf.splitDownNeeds')}
					aria-label={t('desk.leaf.splitDown')}
					onpointerdown={stop}
					onclick={() => multi && onsplit('bottom')}>{I.splitDown}</button
				>
				<button
					class="btn-icon"
					title={maxed ? t('desk.leaf.restore') : t('desk.leaf.maximise')}
					aria-label={maxed ? t('desk.leaf.restore') : t('desk.leaf.maximise')}
					onpointerdown={stop}
					onclick={onmax}>{maxed ? I.restore : I.maximise}</button
				>
				<button
					class="btn-icon big"
					title={t('desk.leaf.hideThis')}
					aria-label={t('desk.leaf.hideThis')}
					onpointerdown={stop}
					onclick={onclose}>{I.close}</button
				>
			</div>{/if}
	</div>
</section>

<style>
	.leaf {
		position: absolute;
		display: flex;
		flex-direction: column;
		border-radius: var(--r-pane);
		overflow: hidden;
	}
	.leaf.focused {
		border-color: var(--brandLn);
	}
	/* Discrete layout changes: the rect springs from wherever it is now. */
	.spring {
		transition-property: left, top, width, height;
		transition-duration: var(--dur-spring);
		transition-timing-function: var(--spring);
	}
	/* A gutter released past its limit springs back with the bounce. */
	.bounce {
		transition-property: left, top, width, height;
		transition-duration: var(--dur-bounce);
		transition-timing-function: var(--spring-bounce);
	}
	.strip {
		flex: none;
		height: var(--leaf-head);
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 0 5px 0 6px;
		border-bottom: 1px solid var(--cLn);
	}
	/* More tabs than room: the row scrolls sideways, without a bar. */
	.tabrow {
		flex: 1 1 auto;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: 2px;
		overflow-x: auto;
		overflow-y: hidden;
		scrollbar-width: none;
		overscroll-behavior-x: contain;
	}
	.tabrow::-webkit-scrollbar {
		display: none;
	}
	/* A tab is clicked far more than it is dragged: the pointer says so, and
	   the grab shows only while one is being moved. */
	.tab {
		flex: none;
		cursor: pointer;
		user-select: none;
		touch-action: none;
		white-space: nowrap;
		padding: 3px 4px 3px 10px;
		border-radius: var(--r-tab);
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: var(--fs-ui);
		font-weight: 500;
		color: var(--mut);
		outline-offset: -2px;
	}
	.tab:hover {
		color: var(--tx);
	}
	.tab.on {
		color: var(--tx);
		font-weight: 600;
	}
	/* A finger swipes the row and taps a tab; there is no drag to start. */
	.compact .tab {
		touch-action: pan-x;
		padding: 5px 12px;
	}
	.compact .strip {
		padding: 0 4px;
	}
	/* Its ends fade: there is more to swipe to. */
	.compact .tabrow {
		mask-image: linear-gradient(
			to right,
			transparent,
			#000 14px,
			#000 calc(100% - 14px),
			transparent
		);
	}
	.tab.ghosted {
		opacity: var(--o-ghost);
		cursor: grabbing;
	}
	.count {
		padding: 0 5px;
		min-width: 0;
	}
	.x {
		width: 17px;
		height: 17px;
		border-radius: var(--r-tag);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: var(--fs-body);
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
		max-width: 40%;
	}
	.ctl {
		flex: none;
		display: flex;
		gap: 1px;
	}
	.btn-icon.dim {
		opacity: var(--o-disabled);
		cursor: default;
	}
	.btn-icon.big {
		font-size: var(--fs-lead);
	}
</style>
