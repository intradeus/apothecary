<script lang="ts">
	import { t } from '$lib/i18n/index.svelte';
	import { portLabel } from '$lib/format';
	// A workshop's repository / provisioner / harness chips (DESIGN 7.7).
	import type { Integration, Project } from '$lib/domain';

	let { project, integrations }: { project: Project; integrations: Integration[] | null } =
		$props();

	const byId = $derived(new Map((integrations ?? []).map((i) => [i.id, i])));
	const name = (id: string) =>
		byId.get(id)?.name ?? (integrations ? t('workshop.chips.missing') : '…');
	const missing = (id: string) => !!integrations && !byId.has(id);

	const chips = $derived([
		{ k: 'repository', ids: [project.repoAccountId] },
		{ k: 'provisioner', ids: [project.provisionerId] },
		{ k: 'harness', ids: project.harnessIds ?? [] }
	]);
</script>

<span class="chips wc">
	{#each chips as c (c.k)}
		{#if c.ids.length === 0}
			<span class="pill sm t-gold" title={t('workshop.chips.noneTitle', { kind: portLabel(c.k) })}
				><span class="k">{portLabel(c.k)}</span>{t('workshop.chips.none')}</span
			>
		{:else}
			{#each c.ids as id (id)}
				<span
					class="pill sm {missing(id) ? 't-ros' : 't-mut'}"
					title={missing(id)
						? t('workshop.chips.missingTitle', { kind: portLabel(c.k) })
						: `${portLabel(c.k)}: ${byId.get(id)?.kind ?? ''}`}
					><span class="k">{portLabel(c.k)}</span>{name(id)}</span
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
		max-width: 300px;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	/* The port word reads quieter than the integration's name. */
	.k {
		opacity: 0.75;
	}
</style>
