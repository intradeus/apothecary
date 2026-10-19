<script lang="ts">
	// The Layouts popover (DESIGN §6): presets, the Windows checklist and how
	// to arrange things by hand. Saved in this browser. Picking a preset
	// springs every window to its new place; the bead slides to the pick.
	import { t } from '$lib/i18n/index.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import StatusMark from '$lib/ui/StatusMark.svelte';
	import { indicator } from '$lib/ui/motion';
	import type { DeskState } from './desk.svelte';
	import { PRESETS, PRESET_IDS, VIEWS, VIEW_LABEL } from './tree';

	let { desk, anchor }: { desk: DeskState; anchor: HTMLElement | null } = $props();
</script>

<Popover
	{anchor}
	bind:open={desk.menuOpen}
	placement="below-end"
	width={300}
	title={t('desk.layouts.title')}
	legend={t('desk.layouts.legend')}
>
	<div class="presets" use:indicator>
		{#each PRESET_IDS as id (id)}
			<button
				class="mi pre"
				class:on={desk.preset === id}
				aria-pressed={desk.preset === id}
				onclick={() => desk.applyPreset(id)}>{PRESETS[id].label}</button
			>
		{/each}
		<button
			class="mi pre"
			class:on={desk.preset === 'custom'}
			aria-pressed={desk.preset === 'custom'}
			title={t('desk.layouts.customTip')}
			disabled={desk.preset !== 'custom'}>{t('desk.preset.custom')}</button
		>
	</div>
	<div class="menu-div"></div>
	<div class="sub lbl">
		<span>{t('desk.layouts.windows')}</span><span
			>{t('desk.layouts.shown', { shown: desk.visible.size, total: VIEWS.length })}</span
		>
	</div>
	<div class="wins">
		{#each VIEWS as v (v)}
			{@const on = desk.visible.has(v)}
			<button
				class="mi"
				role="menuitemcheckbox"
				aria-checked={on}
				onclick={() => desk.toggleView(v)}
			>
				<StatusMark tone={on ? 'ok' : 'pending'} size="sm" />
				<span class:mut={!on}>{VIEW_LABEL[v]}</span>
			</button>
		{/each}
	</div>
	<div class="menu-div"></div>
	<div class="help">
		{t('desk.layouts.help')}
	</div>
</Popover>

<style>
	.presets {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 4px;
	}
	.pre {
		justify-content: center;
		padding: 5px 8px;
		border-radius: var(--r-tab);
		font-size: var(--fs-label);
		color: var(--mut);
	}
	.pre.on {
		color: var(--tx);
	}
	.pre:disabled {
		cursor: default;
	}
	.pre:disabled:not(.on) {
		opacity: var(--o-dim);
	}
	.sub {
		padding: 4px 8px 2px;
		display: flex;
		justify-content: space-between;
	}
	.wins {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 2px;
	}
	.wins .mi {
		gap: 8px;
		padding: 5px 8px;
	}
	.help {
		padding: 4px 8px 4px;
		font-size: var(--fs-meta);
		color: var(--mut);
		line-height: 1.45;
		white-space: normal;
	}
</style>
