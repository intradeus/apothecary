<script lang="ts">
	// The theme picker (DESIGN §5): the one appearance control users get, in
	// the user card. The theme's two hues and its name; on the rail, the hues
	// alone. The popover is portaled: the sidebar's glass would clip it.
	import { prefs } from '$lib/prefs.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { THEMES } from '$lib/themes';
	import Popover from '$lib/ui/Popover.svelte';
	import { I } from '$lib/ui/icons';

	let { rail = false }: { rail?: boolean } = $props();

	let btn = $state<HTMLButtonElement | null>(null);
	let open = $state(false);
	function pick(id: string) {
		open = false;
		prefs.setTheme(id);
	}
</script>

{#snippet swatch(pair: readonly [string, string])}
	<span class="sw" aria-hidden="true"
		><span style:background={pair[0]}></span><span style:background={pair[1]}></span></span
	>
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
	{@render swatch(prefs.themeDef.pair)}
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
		<button class="mi" class:on={th.id === prefs.theme} onclick={() => pick(th.id)}>
			{@render swatch(th.pair)}<span>{th.label}</span>
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
		color: var(--mut);
	}
	.tp:hover {
		background: var(--p3);
		color: var(--tx);
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
		color: var(--fnt);
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
	.sw > span + span {
		margin-left: -3px;
	}
	button.mi {
		width: 100%;
		font-weight: 400;
	}
</style>
