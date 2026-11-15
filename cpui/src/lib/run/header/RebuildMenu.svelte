<script lang="ts">
	// The rebuild menu: the workspace from its image cache, or from scratch.
	import { t } from '$lib/i18n/index.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import type { DeskState } from '$lib/desk/deskState.svelte';
	import type { RunStore } from '../store.svelte';
	import type { HeaderAct } from './actions.svelte';
	import { rebuild as rebuildRun } from './rebuild';

	let {
		rs,
		desk,
		act,
		anchor,
		open = $bindable(false)
	}: {
		rs: RunStore;
		desk: DeskState;
		act: HeaderAct;
		anchor: HTMLElement | null;
		open: boolean;
	} = $props();

	const run = $derived(rs.run!);

	function rebuild(noCache: boolean) {
		open = false;
		return rebuildRun(run.id, noCache, { act, desk });
	}
</script>

<Popover
	{anchor}
	bind:open
	placement="below-end"
	width={300}
	title={t('run.rebuild.popTitle')}
	legend={run.envDirty ? t('run.rebuild.legendDirty') : t('run.rebuild.legendClean')}
>
	<button class="menu-item" onclick={() => rebuild(false)}>
		<span class="col wrap">
			<span>{t('run.rebuild.workspace')}</span>
			<span class="sub">{t('run.rebuild.workspaceSub')}</span>
		</span>
	</button>
	<button class="menu-item" onclick={() => rebuild(true)}>
		<span class="col wrap">
			<span>{t('run.rebuild.noCache')}</span>
			<span class="sub">{t('run.rebuild.noCacheSub')}</span>
		</span>
	</button>
</Popover>

<style>
	/* Menu rows don't wrap; these explain themselves in a sentence. */
	.wrap {
		white-space: normal;
	}
	.wrap .sub {
		line-height: 1.35;
	}
</style>
