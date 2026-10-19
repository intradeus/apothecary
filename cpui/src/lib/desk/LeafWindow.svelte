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
	import type { TransitionConfig } from 'svelte/transition';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/ui/icons';
	import { bump, indicator, ms, spring } from '$lib/ui/motion';
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
	} = $props();

	const multi = $derived(leaf.tabs.length > 1);
	const hint = $derived(w > 420 ? (meta(leaf.act).hint ?? '') : '');
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
		<div class="tabrow" role="tablist" use:indicator>
			{#each leaf.tabs as v (v)}
				{@const m = meta(v)}
				<div
					class="tab"
					class:on={v === leaf.act}
					class:ghosted={dragging === v}
					role="tab"
					tabindex="0"
					aria-selected={v === leaf.act}
					title={t('desk.tab.drag')}
					onpointerdown={(e) => ontabdown(v, e)}
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
					<span
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
					>
				</div>
			{/each}
		</div>
		{#if hint}<span class="hint tiny fnt ell">{hint}</span>{/if}
		<div class="ctl">
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
		</div>
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
	.tab.ghosted {
		opacity: var(--o-ghost);
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
