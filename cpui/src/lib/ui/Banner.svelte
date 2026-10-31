<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	// In-flow alert banner (DESIGN 4.7): window glass over a hue tint.
	import type { Snippet } from 'svelte';

	let {
		tone = 'danger',
		title,
		aside,
		items = [],
		maxItems = 3,
		action,
		onaction,
		ondismiss,
		children
	}: {
		tone?: 'danger' | 'attention';
		title: string;
		/** Display lore aside after the title. */
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

<div class="bn glass {tone}" role="alert">
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
	{#if (action && onaction) || ondismiss}
		<span class="acts">
			{#if action && onaction}
				<button class={tone === 'danger' ? 'btn-danger-solid' : 'btn-primary'} onclick={onaction}
					>{action}</button
				>
			{/if}
			{#if ondismiss}<button class="btn-ghost" onclick={ondismiss}>{t('ui.dismiss')}</button>{/if}
		</span>
	{/if}
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
	.danger {
		background: var(--danger-solid);
		border-color: var(--danger-line);
	}
	.attention {
		background: var(--attention-solid);
		border-color: var(--attention-line);
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
	.danger .bang {
		background: var(--danger);
		color: var(--on-danger);
		box-shadow: 0 0 10px var(--danger);
	}
	.attention .bang {
		background: var(--attention);
		color: var(--on-brand);
		box-shadow: 0 0 10px var(--attention);
	}
	.t {
		font-weight: 700;
		white-space: nowrap;
	}
	.danger .t {
		color: var(--danger-text);
	}
	.attention .t {
		color: var(--attention-text);
	}
	.aside {
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--fs-aside);
		color: var(--text-muted);
		white-space: nowrap;
	}
	.items {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--text);
	}
	.more {
		color: var(--text-muted);
	}
	.acts {
		margin-left: auto;
		flex: none;
		display: flex;
		gap: 6px;
	}
	/* A phone: the alert on top, the hosts under it, the buttons last —
	   never pushed off the edge. */
	@media (max-width: 760px) {
		.bn {
			flex-wrap: wrap;
			row-gap: 6px;
			padding: 7px 7px 7px 9px;
		}
		.aside {
			white-space: normal;
			min-width: 0;
		}
		.items {
			flex: 1 1 100%;
			padding-left: 28px;
		}
	}
</style>
