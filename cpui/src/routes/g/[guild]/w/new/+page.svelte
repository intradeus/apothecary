<script lang="ts">
	// New workshop (DESIGN 7.9, create).
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { Flow, Integration } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { currentGuild, to } from '$lib/guild';
	import ProjectForm from '$lib/ProjectForm.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';

	let integrations = $state<Integration[]>([]);
	let flows = $state<Flow[]>([]);
	let loaded = $state(false);
	let error = $state('');

	async function load() {
		error = '';
		try {
			// A workshop that does not exist yet owns no flows: it starts on a
			// template and authors or copies its own afterwards.
			[integrations, flows] = await Promise.all([api.integrations.list(), api.flows.templates()]);
			loaded = true;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}
	onMount(load);

	const guild = $derived(currentGuild());
	const missing = $derived(
		(['forge', 'provisioner', 'harness'] as const).filter(
			(p) => !integrations.some((i) => i.port === p)
		)
	);
</script>

<PageHeader
	crumbs={[
		{ label: guild.name, href: to.desk() },
		{ label: 'Workshops', href: to.workshops() },
		{ label: 'New' }
	]}
	title="Set up a"
	accent="new workshop"
>
	{#snippet meta()}
		<span
			>A repository, the commands its flows call, where runs execute, and what they may reach.</span
		>
	{/snippet}
</PageHeader>

<div class="page">
	{#if error}
		<ErrorCard cause="Couldn’t load integrations and templates" detail={error} onretry={load} />
	{:else if !loaded}
		<Skeleton pane lines={6} />
	{:else if missing.length}
		<EmptyState
			title="Integrations first"
			text="A workshop needs a {missing.join(', ')} integration to run. Add {missing.length === 1
				? 'it'
				: 'them'} in System, then come back."
		>
			<a class="btn btn-primary" href={to.system('integrations')}>Open integrations</a>
		</EmptyState>
	{:else}
		<ProjectForm
			{integrations}
			{flows}
			oncancel={() => goto(to.workshops())}
			onsaved={async (p, envcheck) => {
				await fleet.refresh();
				await goto(to.workshop(p.id, envcheck ? 'health' : undefined));
			}}
		/>
	{/if}
</div>
