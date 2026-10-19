<script lang="ts">
	// Workshop flows tab (DESIGN 7.7): this workshop's own flows, then the
	// built-in templates it has not adopted. New flow, Copy into another
	// workshop, and "Use" a template (adopt a copy) — everything the old
	// cross-project /flows page did, scoped to one workshop.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { Flow, Project } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { to } from '$lib/guild';
	import { ago, fmtAbs } from '$lib/format';
	import CopyFlow from '$lib/CopyFlow.svelte';
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
	const projects = $derived(fleet.projects.length ? fleet.projects : [project]);
	const projectName = (id: string) => fleet.projectById.get(id)?.name ?? project.name;

	async function create(name: string) {
		await api.flows.create(project.id, name, flowSkeleton(name));
		creating = false;
		toast(`Flow ${name} created`);
		await goto(to.flow(project.id, name));
	}
</script>

<div class="row nowrap">
	<p class="lede grow">
		A flow belongs to this workshop: its stages run these commands and its agents know this
		repository. Runs pin the version they start on.
	</p>
	<button class="btn-primary" onclick={() => (creating = true)}>{I.add} New flow</button>
</div>

{#if error && !flows}
	<ErrorCard cause="Couldn’t load flows" detail={error} onretry={onreload} />
{:else if !flows}
	<Skeleton pane lines={4} />
{:else}
	<section class="c pane">
		<div class="pane-head">
			<h2>This workshop’s flows</h2>
			<span class="count">{own.length}</span>
		</div>
		{#if own.length}
			<div class="rows">
				<div class="r head fr">
					<span>Flow</span><span>Version</span><span class="num">Updated</span><span></span>
				</div>
				{#each own as f (f.name)}
					<div class="r fr">
						<span class="nm">
							<a class="mono stretch" href={to.flow(project.id, f.name)}>{f.name}</a>
							{#if f.name === project.defaultFlow}<span
									class="pill sm t-brand"
									title="Runs start on this flow unless one is picked">default</span
								>{/if}
							{#if f.shadowsBuiltin}<span
									class="pill sm t-mut"
									title="This workshop’s own copy of a built-in template; deleting it resets to the template"
									>from template</span
								>{/if}
						</span>
						<span><span class="tag">@{f.hash.slice(0, 8)}</span></span>
						<span class="num mono fnt" title={fmtAbs(f.updatedAt)}>{ago(f.updatedAt)}</span>
						<span class="acts over-stretch">
							<a class="btn btn-sm" href={to.flow(project.id, f.name)}>Edit</a>
							<button
								class="btn-sm"
								title="Copy this flow into another workshop"
								onclick={() => (copying = { flow: f })}>{I.copy} Copy…</button
							>
						</span>
					</div>
				{/each}
			</div>
		{:else}
			<div class="pane-body">
				<EmptyState
					compact
					title="No flows of its own"
					text="Runs use the {project.defaultFlow} template until this workshop authors or adopts one."
				>
					<button class="btn-primary" onclick={() => (creating = true)}>{I.add} New flow</button>
				</EmptyState>
			</div>
		{/if}
	</section>

	{#if templates.length}
		<section class="c pane">
			<div class="pane-head">
				<h2>Built-in templates</h2>
				<span class="count">{templates.length}</span>
				<span class="mut small">compiled in, owned by nobody — using one copies it here</span>
			</div>
			<div class="rows">
				{#each templates as t (t.name)}
					<div class="r fr">
						<span class="nm">
							<a class="mono stretch" href={to.flow(project.id, t.name)}>{t.name}</a>
							<span class="pill sm t-mut">built-in</span>
							{#if t.name === project.defaultFlow}<span class="pill sm t-brand">default</span>{/if}
						</span>
						<span><span class="tag">@{t.hash.slice(0, 8)}</span></span>
						<span class="num fnt small">from the binary</span>
						<span class="acts over-stretch">
							<a class="btn btn-sm" href={to.flow(project.id, t.name)}>View</a>
							<button
								class="btn-sm btn-sel"
								title="Copy this template into the workshop as an editable flow"
								onclick={() => (copying = { flow: t, target: project.id })}
								>Use built-in template</button
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
		title="New flow"
		action="Create & edit"
		taken={own.map((f) => f.name)}
		onsubmit={create}
		onclose={() => (creating = false)}
	>
		<p>Starts from a minimal skeleton — one builder stage — in <b>{project.name}</b>.</p>
	</NameFlowDialog>
{/if}

{#if copying}
	<CopyFlow
		flow={copying.flow}
		{projects}
		target={copying.target}
		sourceLabel={copying.flow.builtin
			? 'the built-in template'
			: projectName(copying.flow.projectId)}
		onclose={() => (copying = null)}
		oncopied={async (projectId, name) => {
			copying = null;
			toast(`Copied as ${name} into ${projectName(projectId)}`);
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
</style>
