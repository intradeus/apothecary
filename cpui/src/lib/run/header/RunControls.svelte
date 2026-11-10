<script lang="ts">
	// The RUN's lifecycle: play, pause and stop while it lives; retry, rerun
	// and archive once it has ended.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { to } from '$lib/core/urls';
	import { t, th } from '$lib/i18n/index.svelte';
	import { shortId } from '$lib/core/format';
	import { I } from '$lib/design/icons';
	import { toast } from '$lib/ui/toast.svelte';
	import GlassGroup from '$lib/ui/GlassGroup.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import TransportIcon from '$lib/ui/TransportIcon.svelte';
	import type { RunStore } from '../store.svelte';
	import type { HeaderAct, HeaderActions } from './actions.svelte';

	let {
		rs,
		actions,
		act,
		onrerun,
		onretried
	}: {
		rs: RunStore;
		actions: HeaderActions;
		act: HeaderAct;
		onrerun: () => void;
		onretried: () => void;
	} = $props();

	const run = $derived(rs.run!);
	const sid = $derived(shortId(run.id));
	const active = $derived(!rs.terminal);

	// The stage a retry re-enters: where the run stopped.
	const retryStage = $derived(
		run.currentStage ??
			(rs.events.findLast((e) => e.type === 'stage_failed')?.payload.stage_id as string | undefined)
	);

	async function stop() {
		const ok = await confirmDialog({
			title: t('run.stop.title', { id: sid }),
			body: `${th('run.stop.lead')}<br><br>${
				run.envId ? th('run.stop.envUp', { env: run.envId }) : t('run.stop.noEnv')
			} ${t('run.stop.kept')}`,
			action: t('run.stop.action')
		});
		if (ok) await act('stop', () => api.runs.abort(run.id));
	}

	async function archive() {
		const ok = await confirmDialog({
			title: t('run.archive.title', { id: sid }),
			body: `${th('run.archive.lead')}<br><br>${
				run.envId ? th('run.archive.envDestroyed', { env: run.envId }) : t('run.archive.noEnv')
			}`,
			action: t('run.archive.action')
		});
		if (ok && (await act('archive', () => api.runs.archive(run.id))))
			toast(t('run.archive.done', { id: sid }), {
				action: run.ticketId
					? { label: t('run.archive.openTicket'), run: () => goto(to.ticket(run.ticketId!)) }
					: undefined
			});
	}

	async function retry() {
		// Same run, same workspace: the page re-subscribes once it continues.
		if (await act('retry', () => api.runs.retry(run.id))) onretried();
	}
</script>

<GlassGroup label={t('run.header.groupRun')}>
	{#if active}
		{#if run.state === 'paused'}
			<button
				class="btn-primary"
				disabled={actions.any}
				aria-busy={actions.has('play')}
				onclick={() => act('play', () => api.runs.play(run.id))}
				title={run.pause?.reason === 'budget'
					? t('run.header.playBudgetTitle', { why: run.pause.note ?? '' })
					: t('run.header.playTitle')}
				><TransportIcon kind="play" />
				{run.pause?.reason === 'budget' ? t('run.header.playBudget') : t('run.header.play')}</button
			>
		{:else if run.state !== 'pending'}
			<button
				disabled={actions.any}
				aria-busy={actions.has('pause')}
				onclick={() => act('pause', () => api.runs.pause(run.id))}
				title={t('run.header.pauseTitle')}
				><TransportIcon kind="pause" /> {t('run.header.pause')}</button
			>
		{/if}
		<button class="btn-danger" disabled={actions.any} aria-busy={actions.has('stop')} onclick={stop}
			><TransportIcon kind="stop" /> {t('run.header.stop')}</button
		>
	{:else}
		{#if run.state === 'failed' || run.state === 'abandoned'}
			<button
				class="btn-primary"
				disabled={actions.any}
				aria-busy={actions.has('retry')}
				onclick={retry}
				title={run.provisionFailed
					? t('run.header.retryProvisionTitle')
					: t('run.header.retryTitle', {
							stage: retryStage ?? t('run.header.theFailedStage')
						})}
				>{I.retry}
				{run.provisionFailed
					? t('run.header.retryProvision')
					: retryStage
						? t('run.header.retryStage', { stage: retryStage })
						: t('run.header.retry')}</button
			>
		{/if}
		<button disabled={actions.any} onclick={() => onrerun()} title={t('run.header.rerunTitle')}
			>{t('run.header.rerun')}</button
		>
		{#if run.state !== 'archived'}
			<button
				class="btn-danger"
				disabled={actions.any}
				aria-busy={actions.has('archive')}
				onclick={archive}>{t('run.header.archive')}</button
			>
		{/if}
	{/if}
</GlassGroup>
