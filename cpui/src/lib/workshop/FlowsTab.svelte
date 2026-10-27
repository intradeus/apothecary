<script lang="ts">
	import { t, th } from '$lib/i18n/index.svelte';
	// Workshop flows tab (DESIGN 7.7): this workshop's own flows, then the
	// built-in templates it has not adopted. New flow, Copy into another
	// workshop, Configure (bind this workshop's values to a flow's
	// parameters — a template as it is, no copy needed), and "Copy to edit" a
	// template — everything the old cross-project /flows page did, scoped to
	// one workshop.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { Flow, Project } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { to } from '$lib/urls';
	import { ago, fmtAbs } from '$lib/format';
	import CopyFlow from '$lib/CopyFlow.svelte';
	import FlowBindingSheet from '$lib/workshop/FlowBindingSheet.svelte';
	import NameFlowDialog from '$lib/flow/NameFlowDialog.svelte';
	import { flowSkeleton } from '$lib/flow/skeleton';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';

	let {
		project,
		flows,
		error,
		onreload
	}: { project: Project; flows: Flow[] | null; error: string; onreload: () => void } = $props();

	const own = $derived(
		(flows ?? []).filter((f) => !f.builtin).sort((a, b) => a.name.localeCompare(b.name))
	);
	const templates = $derived((flows ?? []).filter((f) => f.builtin));

	let creating = $state(false);
	let copying = $state<{ flow: Flow; target?: string } | null>(null);
	let configuring = $state<Flow | null>(null);
	// Which flows this workshop has bound parameters on — a pill, nothing
	// more: the sheet reports its own errors.
	let configured = $state<Set<string>>(new Set());
	async function loadBindings() {
		try {
			configured = new Set((await api.flows.bindings(project.id)).map((b) => b.flowName));
		} catch {
			configured = new Set();
		}
	}
	$effect(() => {
		void project.id;
		void loadBindings();
	});
	const projects = $derived(fleet.projects.length ? fleet.projects : [project]);
	const projectName = (id: string) => fleet.projectById.get(id)?.name ?? project.name;

	async function create(name: string) {
		await api.flows.create(project.id, name, flowSkeleton(name));
		creating = false;
		toast(t('workshop.flows.created', { name }));
		await goto(to.flow(project.id, name));
	}
</script>

<div class="row nowrap">
	<p class="lede grow">
		{t('workshop.flows.lede')}
	</p>
	<button class="btn-primary" onclick={() => (creating = true)}
		>{I.add} {t('workshop.flows.new')}</button
	>
</div>

{#if error && !flows}
	<ErrorCard cause={t('workshop.flows.loadError')} detail={error} onretry={onreload} />
{:else if !flows}
	<Skeleton pane lines={4} />
{:else}
	<section class="c pane">
		<div class="pane-head">
			<h2>{t('workshop.flows.ownTitle')}</h2>
			<span class="count">{own.length}</span>
		</div>
		{#if own.length}
			<div class="rows">
				<div class="r head fr">
					<span>{t('workshop.flows.colFlow')}</span><span>{t('workshop.flows.colVersion')}</span
					><span class="num">{t('workshop.flows.colUpdated')}</span><span></span>
				</div>
				{#each own as f (f.name)}
					<div class="r fr">
						<span class="nm">
							<a class="mono stretch" href={to.flow(project.id, f.name)}>{f.name}</a>
							{#if f.name === project.defaultFlow}<span
									class="pill sm t-brand"
									title={t('workshop.flows.defaultTitle')}>{t('workshop.flows.default')}</span
								>{/if}
							{#if f.shadowsBuiltin}<span
									class="pill sm t-mut"
									title={t('workshop.flows.fromTemplateTitle')}
									>{t('workshop.flows.fromTemplate')}</span
								>{/if}
							{#if configured.has(f.name)}<span
									class="pill sm t-brand"
									title={t('workshop.flows.configuredTitle')}>{t('workshop.flows.configured')}</span
								>{/if}
						</span>
						<span><span class="tag">@{f.hash.slice(0, 8)}</span></span>
						<span class="num mono fnt" title={fmtAbs(f.updatedAt)}>{ago(f.updatedAt)}</span>
						<span class="acts over-stretch">
							<a class="btn btn-sm" href={to.flow(project.id, f.name)}>{t('workshop.flows.edit')}</a
							>
							<button
								class="btn-sm"
								title={t('workshop.flows.configureTitle')}
								onclick={() => (configuring = f)}>{t('workshop.flows.configure')}</button
							>
							<button
								class="btn-sm"
								title={t('workshop.flows.copyTitle')}
								onclick={() => (copying = { flow: f })}>{I.copy} {t('workshop.flows.copy')}</button
							>
						</span>
					</div>
				{/each}
			</div>
		{:else}
			<div class="pane-body">
				<EmptyState
					compact
					title={t('workshop.flows.emptyTitle')}
					text={t('workshop.flows.emptyText', { flow: project.defaultFlow })}
				>
					<button class="btn-primary" onclick={() => (creating = true)}
						>{I.add} {t('workshop.flows.new')}</button
					>
				</EmptyState>
			</div>
		{/if}
	</section>

	{#if templates.length}
		<section class="c pane">
			<div class="pane-head">
				<h2>{t('workshop.flows.templates')}</h2>
				<span class="count">{templates.length}</span>
				<span class="mut small">{t('workshop.flows.templatesNote')}</span>
			</div>
			<div class="rows">
				{#each templates as tpl (tpl.name)}
					<div class="r fr">
						<span class="nm">
							<a class="mono stretch" href={to.flow(project.id, tpl.name)}>{tpl.name}</a>
							<span class="pill sm t-mut">{t('workshop.flows.builtin')}</span>
							{#if tpl.name === project.defaultFlow}<span class="pill sm t-brand"
									>{t('workshop.flows.default')}</span
								>{/if}
							{#if configured.has(tpl.name)}<span
									class="pill sm t-brand"
									title={t('workshop.flows.configuredTitle')}>{t('workshop.flows.configured')}</span
								>{/if}
						</span>
						<span><span class="tag">@{tpl.hash.slice(0, 8)}</span></span>
						<span class="num fnt small">{t('workshop.flows.fromBinary')}</span>
						<span class="acts over-stretch">
							<a class="btn btn-sm" href={to.flow(project.id, tpl.name)}
								>{t('workshop.flows.view')}</a
							>
							<button
								class="btn-sm btn-sel"
								title={t('workshop.flows.configureTitle')}
								onclick={() => (configuring = tpl)}>{t('workshop.flows.configure')}</button
							>
							<button
								class="btn-sm"
								title={t('workshop.flows.useTitle')}
								onclick={() => (copying = { flow: tpl, target: project.id })}
								>{t('workshop.flows.use')}</button
							>
						</span>
					</div>
				{/each}
			</div>
		</section>
	{/if}
{/if}

{#if creating}
	<NameFlowDialog
		title={t('workshop.flows.createTitle')}
		action={t('workshop.flows.createAction')}
		taken={own.map((f) => f.name)}
		onsubmit={create}
		onclose={() => (creating = false)}
	>
		<p>{@html th('workshop.flows.createNote', { name: project.name })}</p>
	</NameFlowDialog>
{/if}

{#if configuring}
	{@const name = configuring.name}
	<FlowBindingSheet
		{project}
		flow={configuring}
		onclose={() => (configuring = null)}
		onsaved={(b) => {
			configuring = null;
			toast(
				b
					? t('workshop.flows.binding.saved', { name })
					: t('workshop.flows.binding.resetDone', { name })
			);
			void loadBindings();
		}}
	/>
{/if}

{#if copying}
	<CopyFlow
		flow={copying.flow}
		{projects}
		target={copying.target}
		sourceLabel={copying.flow.builtin
			? t('workshop.flows.sourceBuiltin')
			: projectName(copying.flow.projectId)}
		onclose={() => (copying = null)}
		oncopied={async (projectId, name) => {
			copying = null;
			toast(t('workshop.flows.copied', { name, project: projectName(projectId) }));
			onreload();
			await goto(to.flow(projectId, name));
		}}
	/>
{/if}

<style>
	.fr {
		grid-template-columns: minmax(0, 1fr) 110px 80px auto;
	}
	.nm {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.nm .mono {
		font-size: var(--fs-ui);
		font-weight: 600;
		color: var(--tx);
	}
	.nm .mono:hover {
		text-decoration: none;
	}
	.acts {
		display: flex;
		gap: 6px;
		justify-content: flex-end;
	}
	/* A phone: the name (and its pills) on a line of its own, version and
	   age under it, the actions last — the columns are gone. */
	@media (max-width: 760px) {
		.fr {
			grid-template-columns: auto minmax(0, 1fr);
			row-gap: 6px;
		}
		.fr.head {
			display: none;
		}
		.nm {
			grid-column: 1 / -1;
			flex-wrap: wrap;
		}
		.fr > .num {
			text-align: left;
		}
		.acts {
			grid-column: 1 / -1;
			justify-content: flex-start;
			flex-wrap: wrap;
		}
	}
</style>
