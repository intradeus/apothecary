<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// In-flow alert banner (DESIGN 4.7): control glass over a hue tint.
	import type { Snippet } from 'svelte';

	let {
		tone = 'ros',
		title,
		aside,
		items = [],
		maxItems = 3,
		action,
		onaction,
		ondismiss,
		children
	}: {
		tone?: 'ros' | 'amb';
		title: string;
		/** Serif lore aside after the title. */
		aside?: string;
		/** Mono list ("host:443 · …"), ellipsised with "+N more". */
		items?: string[];
		maxItems?: number;
		action?: string;
		onaction?: () => void;
		ondismiss?: () => void;
		children?: Snippet;
	} = $props();

	const shown = $derived(items.slice(0, maxItems));
	const more = $derived(items.length - shown.length);
</script>

<div class="bn lg {tone}" role="alert">
	<span class="bang">!</span>
	<span class="t">{title}</span>
	{#if aside}<span class="aside">{aside}</span>{/if}
	{#if shown.length}
		<span class="items mono"
			>{shown.join(' · ')}{#if more > 0}<span class="more">
					{t('ui.banner.more', { count: more })}</span
				>{/if}</span
		>
	{/if}
	{#if children}{@render children()}{/if}
	<span class="spacer"></span>
	{#if action && onaction}
		<button class={tone === 'ros' ? 'btn-danger-solid' : 'btn-primary'} onclick={onaction}
			>{action}</button
		>
	{/if}
	{#if ondismiss}<button class="btn-ghost" onclick={ondismiss}>{t('ui.dismiss')}</button>{/if}
</div>

<style>
	.bn {
		display: flex;
		align-items: center;
		gap: 10px;
		border-radius: var(--r-btn);
		padding: 5px 5px 5px 7px;
		min-width: 0;
	}
	.ros {
		background: var(--rosBg);
		border-color: var(--rosLn);
	}
	.amb {
		background: var(--ambBg);
		border-color: var(--ambLn);
	}
	.bang {
		flex: none;
		width: 18px;
		height: 18px;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: var(--fs-meta);
		font-weight: 800;
	}
	.ros .bang {
		background: var(--ros);
		color: var(--rosInk);
		box-shadow: 0 0 10px var(--ros);
	}
	.amb .bang {
		background: var(--amb);
		color: var(--ink);
		box-shadow: 0 0 10px var(--amb);
	}
	.t {
		font-weight: 700;
		white-space: nowrap;
	}
	.ros .t {
		color: var(--ros);
	}
	.amb .t {
		color: var(--amb);
	}
	.aside {
		font-family: var(--serif);
		font-style: italic;
		font-size: var(--fs-aside);
		color: var(--mut);
		white-space: nowrap;
	}
	.items {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--tx);
	}
	.more {
		color: var(--mut);
	}
	.spacer {
		flex: 1;
	}
</style>
