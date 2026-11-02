<script lang="ts">
	// The rebuild menu: the workspace from its image cache, or from scratch.
	import { api } from '$lib/api';
	import { t } from '$lib/i18n/index.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import type { DeskState } from '$lib/desk/deskState.svelte';
	import type { RunStore } from '../store.svelte';
	import type { HeaderAct } from './actions.svelte';

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

	async function rebuild(noCache: boolean) {
		open = false;
		// The request blocks for the whole build; the badge and the Logs tail
		// show progress meanwhile.
		toast(noCache ? t('run.rebuild.startedNoCache') : t('run.rebuild.started'), {
			tone: 'info',
			action: { label: t('run.rebuild.watchLogs'), run: () => desk.reveal('logs') }
		});
		if (await act('rebuild', () => api.runs.rebuild(run.id, noCache))) toast(t('run.rebuild.done'));
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
