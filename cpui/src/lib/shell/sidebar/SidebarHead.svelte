<script lang="ts">
	// The top of the full sidebar: the brand, its collapse button (not in the
	// drawer, which is always full width) and the command bar.
	import { prefs } from '$lib/app/prefs.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import Logo from '$lib/ui/Logo.svelte';
	import { I } from '$lib/design/icons';
	import ChromeButton from './ChromeButton.svelte';

	let { drawer, onsearch }: { drawer: boolean; onsearch: () => void } = $props();
</script>

<div class="brand">
	<Logo />
	<div class="bt">
		<span class="name">Apothecary</span>
		<span class="tagline">{t('shell.sidebar.tagline')}</span>
	</div>
	{#if !drawer}
		<ChromeButton
			icon={I.collapse}
			size="sm"
			title={t('shell.sidebar.collapseTitle')}
			label={t('shell.sidebar.collapse')}
			onclick={() => prefs.toggleCollapsed()}
		/>
	{/if}
</div>

<button class="search" onclick={onsearch}>
	<span>{t('shell.sidebar.searchPlaceholder')}</span><span class="kbd">⌘K</span>
</button>

<style>
	.brand {
		height: 56px;
		flex: none;
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 0 10px 0 14px;
	}
	.bt {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		line-height: 1.1;
	}
	.name {
		font-weight: 600;
		font-size: var(--fs-lead);
		letter-spacing: var(--tr-heading);
	}
	.tagline {
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--fs-body);
		color: var(--text-muted);
	}

	/* The command bar: a field in rail ink that opens the palette. */
	.search {
		flex: none;
		display: flex;
		justify-content: space-between;
		margin: 0 10px 6px;
		padding: 6px 10px;
		border-radius: var(--r-row);
		background: var(--fill-1);
		border: 1px solid var(--line);
		box-shadow: none;
		color: var(--text-muted);
		font-size: var(--fs-ui);
		font-weight: 400;
	}
	.search:hover {
		background: var(--fill-2);
	}
	.search .kbd {
		background: var(--fill-3);
		color: var(--text-muted);
	}
</style>
