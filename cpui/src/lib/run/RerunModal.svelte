<script lang="ts">
	// "New run from this…" (DESIGN 4.5): the same ticket as a fresh run — new
	// workspace, new branch — with direction for the attempt, files and a
	// flow. The current run is untouched.
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { shortId } from '$lib/format';
	import Modal from '$lib/ui/Modal.svelte';
	import { I } from '$lib/ui/icons';
	import AttachFiles from '$lib/AttachFiles.svelte';
	import FlowSelect from '$lib/FlowSelect.svelte';
	import type { RunStore } from './store.svelte';
	import { t, th } from '$lib/i18n/index.svelte';

	let {
		rs,
		open = $bindable(false),
		onstarted
	}: { rs: RunStore; open: boolean; onstarted: (runId: string) => void } = $props();

	let goal = $state('');
	let flow = $state('');
	let files = $state<File[]>([]);
	let busy = $state(false);
	let error = $state('');

	async function start() {
		error = '';
		busy = true;
		try {
			let attachmentIds: string[] | undefined;
			if (files.length) attachmentIds = (await api.files.upload(files)).map((a) => a.id);
			const id = await api.runs.rerun(rs.runId, {
				flow: flow.trim() || undefined,
				goal: goal.trim() || undefined,
				attachmentIds
			});
			void fleet.refresh();
			open = false;
			goal = '';
			files = [];
			onstarted(id);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}
</script>

<Modal bind:open title={t('run.rerun.title', { id: shortId(rs.runId) })} icon={I.retry} width={520}>
	<div class="f">
		<p class="lead">
			{@html th('run.rerun.lead')}
		</p>
		<div class="field">
			<label for="nr-goal">{t('run.rerun.direction')}</label>
			<textarea id="nr-goal" bind:value={goal} rows="3" placeholder={t('run.rerun.directionPh')}
			></textarea>
			<span class="help">{t('run.rerun.directionHelp')}</span>
		</div>
		<AttachFiles bind:files label={t('run.rerun.attach')} compact />
		<div class="field">
			<label for="nr-flow">{t('run.rerun.flow')}</label>
			{#if rs.run?.projectId}
				<FlowSelect
					id="nr-flow"
					projectId={rs.run.projectId}
					bind:value={flow}
					fallbackLabel={t('run.rerun.sameFlow', { flow: rs.run.flowName })}
				/>
			{:else}
				<input id="nr-flow" class="mono" bind:value={flow} placeholder={rs.run?.flowName} />
			{/if}
		</div>
		{#if error}<div class="err">{error}</div>{/if}
	</div>
	{#snippet footer()}
		<button class="btn-ghost" onclick={() => (open = false)}>{t('common.cancel')}</button>
		<button class="btn-primary" disabled={busy} aria-busy={busy} onclick={start}
			>{t('run.rerun.start')}</button
		>
	{/snippet}
</Modal>

<style>
	.f {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.lead {
		margin: 0;
		color: var(--mut);
		text-wrap: pretty;
	}
	.lead :global(b) {
		color: var(--tx);
		font-weight: 600;
	}
</style>
