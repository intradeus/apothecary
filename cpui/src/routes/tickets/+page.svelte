<script lang="ts">
	import { api } from '$lib/api';
	import { ago } from '$lib/format';
	import type { Project, Ticket } from '$lib/domain';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';

	let tickets = $state<Ticket[]>([]);
	let projects = $state<Project[]>([]);
	let loaded = $state(false);
	let error = $state('');

	onMount(async () => {
		try {
			[tickets, projects] = await Promise.all([api.tickets.list(), api.projects.list()]);
			loaded = true;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	});

	const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? id.slice(-6);
</script>

<div class="row spread" style="margin-bottom:16px">
	<h1 style="margin:0">Tickets</h1>
	<button class="primary" onclick={() => goto('/tickets/new')}>+ New ticket</button>
</div>

{#if error}<div class="error">{error}</div>{/if}

{#each tickets as t (t.id)}
	<a class="card row spread cardlink" href={`/tickets/${t.id}`}>
		<div>
			<strong>{t.title}</strong>
			<div class="small muted">
				{#if t.source?.key}<span class="chip mono">{t.source?.key}</span>{/if}
				{projectName(t.projectId)}
				{#if t.baseBranch}· <span class="mono">{t.baseBranch}</span>{/if}
				· {ago(t.createdAt)}
			</div>
		</div>
		<span class="muted">→</span>
	</a>
{:else}
	<div class="empty">
		{#if !loaded}
			Loading…
		{:else}
			No tickets yet. Create the first one to start runs against it.
		{/if}
	</div>
{/each}
