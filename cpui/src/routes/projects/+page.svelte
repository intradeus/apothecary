<script lang="ts">
	import { api } from '$lib/api';
	import type { Integration, Project } from '$lib/domain';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';

	let projects = $state<Project[]>([]);
	let integrations = $state<Integration[]>([]);
	let loaded = $state(false);
	let error = $state('');

	onMount(async () => {
		try {
			[projects, integrations] = await Promise.all([api.projects.list(), api.integrations.list()]);
			loaded = true;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	});

	const intName = (id: string) => integrations.find((i) => i.id === id)?.name ?? id.slice(-6);
</script>

<div class="row spread" style="margin-bottom:16px">
	<h1 style="margin:0">Projects</h1>
	<button class="primary" onclick={() => goto('/projects/new')}>+ New project</button>
</div>

{#if error}<div class="error">{error}</div>{/if}

{#each projects as p (p.id)}
	<a class="card row spread cardlink" href={`/projects/${p.id}`}>
		<div>
			<strong>{p.name}</strong>
			<div class="mono small muted">{p.repo.url}</div>
			<div class="small muted">
				forge: {intName(p.forgeId)} · workspaces: {intName(p.provisionerId)} · harnesses:
				{(p.harnessIds ?? []).map(intName).join(', ') || '—'} · flow: {p.defaultFlow}
			</div>
		</div>
		<span class="muted">→</span>
	</a>
{:else}
	<div class="empty">
		{#if !loaded}
			Loading…
		{:else}
			No projects yet. Configure <a href="/integrations">integrations</a>, then create the first
			project.
		{/if}
	</div>
{/each}
