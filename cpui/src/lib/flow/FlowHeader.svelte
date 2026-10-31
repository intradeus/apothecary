<script lang="ts">
	// The editor's header: the flow's name @hash and facts, then Graph | Agents |
	// YAML, Validate, Versions, Clone, Delete… / Reset, Save. Validate's popover
	// hangs off its button.
	import { fleet } from '$lib/app/fleet.svelte';
	import { ago, fmtAbs } from '$lib/core/format';
	import { to } from '$lib/core/urls';
	import type { FlowEditor } from '$lib/flow/flowEditor.svelte';
	import ValidationPopover from '$lib/flow/ValidationPopover.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import GlassGroup from '$lib/ui/GlassGroup.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';

	let { ed }: { ed: FlowEditor } = $props();

	const flow = $derived(ed.flow);
	const doc = $derived(ed.doc);
	const validation = $derived(ed.validation);
	const project = $derived(fleet.projectById.get(ed.projectId));
	const workshopName = $derived(project?.name ?? '…');

	let validateBtn = $state<HTMLButtonElement | null>(null);
</script>

<PageHeader
	crumbs={[
		{ label: t('nav.desk'), href: to.desk() },
		{ label: workshopName, href: to.workshop(ed.projectId) },
		{ label: t('page.workshops.flows'), href: to.workshop(ed.projectId, 'flows') },
		{ label: ed.name, mono: true }
	]}
>
	<div class="trow">
		<h1 class="mono fname">{flow?.name ?? ed.name}</h1>
		{#if flow}<span class="tag hash" title={t('page.flow.hashTitle')}>@{flow.hash.slice(0, 8)}</span
			>{/if}
		{#if flow?.builtin}<span class="pill tint-muted">{t('page.flow.builtin')}</span>{/if}
		{#if flow?.shadowsBuiltin}<span class="pill tint-muted">{t('page.flow.fromTemplate')}</span
			>{/if}
		{#if flow && project?.defaultFlow === flow.name}<span class="pill tint-brand"
				>{t('page.flow.default')}</span
			>{/if}
	</div>
	<div class="meta">
		{#if doc?.description}<span class="truncate desc">{doc.description}</span>{/if}
		{#if doc}<span>{t('page.flow.stages', { count: ed.stageCount })}</span>{/if}
		{#if flow}<span title={fmtAbs(flow.updatedAt)}
				>{t('page.flow.savedAgo', { ago: ago(flow.updatedAt) })}</span
			>{/if}
	</div>
	{#snippet actions()}
		{#if flow}
			<div class="vc">
				<Segmented
					options={[
						{ value: 'graph', label: t('page.flow.graph') },
						{ value: 'agents', label: t('page.flow.agents') },
						{ value: 'yaml', label: 'YAML' }
					]}
					value={ed.tab}
					onchange={(v) => ed.switchTab(v)}
				/>
			</div>
			<GlassGroup>
				{#snippet label()}
					{#if ed.dirty}
						<span class="unsaved">{t('page.flow.unsaved')}</span>
					{:else}
						<span>{t('page.flow.formula')}</span>
					{/if}
				{/snippet}
				<button
					bind:this={validateBtn}
					class="btn-sm"
					class:btn-danger={validation && !validation.ok}
					aria-busy={ed.validating}
					onclick={ed.validate}
					>{t('page.flow.validate')}{#if validation?.warnings?.length}<span
							class="pill sm mono tint-attention">{validation.warnings.length}</span
						>{/if}</button
				>
				<button
					class="btn-sm"
					class:btn-sel={ed.showVersions}
					aria-pressed={ed.showVersions}
					onclick={() => (ed.showVersions = !ed.showVersions)}
					>{t('page.flow.versions')} <span class="mono faint">{ed.versions.length}</span></button
				>
				<button class="btn-sm" onclick={() => (ed.cloning = true)} disabled={ed.busy}
					>{t('page.flow.clone')}</button
				>
				{#if flow.shadowsBuiltin}
					<button class="btn-sm btn-danger" onclick={ed.remove}
						>{t('page.flow.resetAction')}…</button
					>
				{:else if !flow.builtin}
					<button class="btn-sm btn-danger" onclick={ed.remove}>{t('page.ticket.delete')}</button>
				{/if}
				<button
					class="btn-sm btn-primary"
					onclick={ed.save}
					disabled={ed.busy || !ed.dirty}
					aria-busy={ed.busy}
					title={flow.builtin ? t('page.flow.saveCopyTitle') : '⌘S'}
					>{ed.busy
						? t('page.flow.saving')
						: flow.builtin
							? t('page.flow.saveAsCopy')
							: t('common.save')}</button
				>
			</GlassGroup>
		{/if}
	{/snippet}
</PageHeader>

<ValidationPopover anchor={validateBtn} {validation} bind:open={ed.validateOpen} />

<style>
	.trow {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}
	.fname {
		font-family: var(--font-code);
		font-size: var(--fs-mark);
		font-weight: 600;
		letter-spacing: var(--tr-heading);
	}
	.hash {
		font-size: var(--fs-help);
		color: var(--text-muted);
	}
	.meta {
		display: flex;
		gap: 4px 14px;
		flex-wrap: wrap;
		font-size: var(--fs-label);
		color: var(--text-muted);
		min-width: 0;
	}
	.desc {
		max-width: 60ch;
	}
	.vc {
		display: flex;
		align-items: center;
	}
</style>
