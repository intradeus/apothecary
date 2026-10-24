<script lang="ts" module>
	export interface SideNavItem {
		id: string;
		label: string;
		href?: string;
		icon?: string;
		/** A quieter second line. */
		sub?: string;
		/** A step number (stepped forms); `done` ticks it. */
		step?: number;
		done?: boolean;
		count?: number;
		hot?: boolean;
	}
</script>

<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// A vertical section nav (System sections, a stepped form's steps). One
	// bead springs between items as the selection moves (ui/motion
	// `indicator`). Below 1000px it becomes a horizontal scroller.
	import { indicator } from './motion';
	import { keepInView } from './inview';
	import { I } from './icons';

	let {
		items,
		active,
		title,
		onselect,
		label
	}: {
		items: SideNavItem[];
		active: string;
		title?: string;
		/** Called for items without an href (in-page steps). */
		onselect?: (id: string) => void;
		label?: string;
	} = $props();
</script>

<nav class="sn lg" aria-label={label ?? t('ui.sidenav.label')}>
	{#if title}<div class="t">{title}</div>{/if}
	<div class="list" use:indicator use:keepInView>
		{#each items as it (it.id)}
			{#snippet inner()}
				{#if it.step !== undefined}
					<span class="n" class:done={it.done}>{it.done ? I.ok : it.step}</span>
				{:else if it.icon}
					<span class="ic">{it.icon}</span>
				{/if}
				<span class="col">
					<span class="ell">{it.label}</span>
					{#if it.sub}<span class="sub ell">{it.sub}</span>{/if}
				</span>
				{#if it.count}<span class="count" class:hot={it.hot}>{it.count}</span>{/if}
			{/snippet}
			{#if it.href}
				<a
					class="si"
					class:on={it.id === active}
					href={it.href}
					aria-current={it.id === active ? 'page' : undefined}>{@render inner()}</a
				>
			{:else}
				<button
					type="button"
					class="si"
					class:on={it.id === active}
					aria-current={it.id === active ? 'step' : undefined}
					onclick={() => onselect?.(it.id)}>{@render inner()}</button
				>
			{/if}
		{/each}
	</div>
</nav>

<style>
	.sn {
		flex: none;
		width: 220px;
		align-self: flex-start;
		position: sticky;
		top: 0;
		border-radius: var(--r-pane);
		padding: 8px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.t {
		font-family: var(--serif);
		font-style: italic;
		font-size: var(--fs-aside);
		color: var(--mut);
		padding: 4px 10px 4px;
	}
	.list {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.list > :global(.ind) {
		border-radius: var(--r-row);
	}
	.si {
		all: unset;
		/* all: unset also drops the global .has-ind lift — without it the
		   absolutely positioned bead paints over the selected item. */
		position: relative;
		z-index: var(--z-raise);
		box-sizing: border-box;
		cursor: pointer;
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 6px 10px;
		border-radius: var(--r-row);
		color: var(--tx);
		font-size: var(--fs-ui);
		font-weight: 500;
		min-width: 0;
		-webkit-tap-highlight-color: transparent;
	}
	.si:hover {
		background: var(--p1);
		text-decoration: none;
	}
	.si:active {
		opacity: 0.75;
	}
	.si:focus-visible {
		outline: 2px solid var(--brand);
		outline-offset: -2px;
	}
	.si.on {
		font-weight: 600;
	}
	.ic {
		width: 16px;
		text-align: center;
		color: var(--mut);
		flex: none;
	}
	.col {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
		line-height: 1.25;
	}
	.sub {
		font-size: var(--fs-meta);
		color: var(--mut);
		font-weight: 400;
	}
	.n {
		flex: none;
		width: 20px;
		height: 20px;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		font-family: var(--mono);
		font-size: var(--fs-id);
		border: 1px solid var(--ln2);
		color: var(--mut);
		transition:
			background var(--dur-fade) var(--ease-out),
			color var(--dur-fade) var(--ease-out);
	}
	.n.done {
		background: var(--grn);
		border-color: transparent;
		color: var(--deepInk);
	}
	@media (max-width: 1000px) {
		.sn {
			width: auto;
			position: static;
			align-self: stretch;
		}
		.t {
			display: none;
		}
		/* A row of whole labels that scrolls, never squeezed ones. */
		.list {
			flex-direction: row;
			overflow-x: auto;
			scrollbar-width: none;
			mask-image: linear-gradient(
				to right,
				transparent,
				#000 10px,
				#000 calc(100% - 10px),
				transparent
			);
		}
		.list::-webkit-scrollbar {
			display: none;
		}
		.si {
			flex: none;
		}
		.sub {
			display: none;
		}
	}
</style>
