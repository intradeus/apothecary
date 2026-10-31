<script lang="ts">
	// The theme picker (DESIGN §5): the one appearance control users get, in
	// the user card. The theme's two hues and its name; on the rail, the hues
	// alone. The popover is portaled: the sidebar's glass would clip it.
	import { prefs } from '$lib/app/prefs.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { THEMES } from '$lib/design/themes';
	import Popover from '$lib/ui/Popover.svelte';
	import { I } from '$lib/design/icons';

	let { rail = false }: { rail?: boolean } = $props();

	let btn = $state<HTMLButtonElement | null>(null);
	let open = $state(false);
	function pick(id: string) {
		open = false;
		prefs.setTheme(id);
	}
</script>

<!-- data-theme makes the swatch resolve that theme's own roles (themes.css). -->
{#snippet swatch(id: string)}
	<span class="sw" data-theme={id} aria-hidden="true"><span></span><span></span></span>
{/snippet}

<button
	bind:this={btn}
	class="tp"
	class:rail
	title={t('shell.sidebar.themeTitle', { name: prefs.themeDef.label })}
	aria-label={t('shell.sidebar.changeTheme')}
	aria-expanded={open}
	onclick={() => (open = !open)}
>
	{@render swatch(prefs.themeDef.id)}
	{#if !rail}<span class="nm">{prefs.themeDef.label}</span><span class="cv">{I.menu}</span>{/if}
</button>

<Popover
	anchor={btn}
	bind:open
	portal
	flip
	placement="above-start"
	width={210}
	title={t('shell.sidebar.theme')}
>
	{#each THEMES as th (th.id)}
		<button class="menu-item" class:on={th.id === prefs.theme} onclick={() => pick(th.id)}>
			{@render swatch(th.id)}<span>{th.label}</span>
		</button>
	{/each}
</Popover>

<style>
	.tp {
		justify-content: flex-start;
		gap: 9px;
		min-width: 0;
		padding: 4px 7px;
		background: none;
		border: 0;
		box-shadow: none;
		border-radius: var(--r-tab);
		font-size: var(--fs-help);
		font-weight: 500;
		color: var(--text-muted);
	}
	.tp:hover {
		background: var(--fill-3);
		color: var(--text);
	}
	.tp.rail {
		width: 40px;
		height: 26px;
		padding: 0;
		justify-content: center;
	}
	/* Lines the hues up over the avatar in the row above. */
	.tp:not(.rail) .sw {
		width: 30px;
		justify-content: center;
	}
	.nm {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		text-align: left;
	}
	.cv {
		flex: none;
		color: var(--text-faint);
		font-size: var(--fs-meta);
	}
	.sw {
		flex: none;
		display: inline-flex;
	}
	.sw > span {
		width: 10px;
		height: 10px;
		border-radius: 50%;
	}
	.sw > span:first-child {
		background: var(--running);
	}
	.sw > span + span {
		margin-left: -3px;
		background: var(--attention);
	}
	button.menu-item {
		width: 100%;
		font-weight: 400;
	}
</style>
