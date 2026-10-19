<script lang="ts">
	// A workshop's forge / provisioner / harness chips (DESIGN 7.7).
	import type { Integration, Project } from '$lib/domain';

	let { project, integrations }: { project: Project; integrations: Integration[] | null } =
		$props();

	const byId = $derived(new Map((integrations ?? []).map((i) => [i.id, i])));
	const name = (id: string) => byId.get(id)?.name ?? (integrations ? 'missing' : '…');
	const missing = (id: string) => !!integrations && !byId.has(id);

	const chips = $derived([
		{ k: 'forge', ids: [project.forgeId] },
		{ k: 'provisioner', ids: [project.provisionerId] },
		{ k: 'harness', ids: project.harnessIds ?? [] }
	]);
</script>

<span class="chips wc">
	{#each chips as c (c.k)}
		{#if c.ids.length === 0}
			<span class="pill sm t-amb" title="No {c.k} integration picked"
				><span class="k">{c.k}</span>none</span
			>
		{:else}
			{#each c.ids as id (id)}
				<span
					class="pill sm {missing(id) ? 't-ros' : 't-mut'}"
					title={missing(id)
						? `The ${c.k} integration this workshop names no longer exists`
						: `${c.k}: ${byId.get(id)?.kind ?? ''}`}><span class="k">{c.k}</span>{name(id)}</span
				>
			{/each}
		{/if}
	{/each}
</span>

<style>
	.wc {
		min-width: 0;
	}
	.pill {
		font-weight: 500;
		max-width: 300px;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	/* The port word reads quieter than the integration's name. */
	.k {
		opacity: 0.75;
	}
</style>
