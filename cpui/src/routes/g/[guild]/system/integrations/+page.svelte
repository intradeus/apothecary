<script lang="ts">
	// Integrations (DESIGN 7.10): four groups — forge, provisioner, harness,
	// ticket source — each with "+ Add <kind>" per registered kind and a card
	// per configured integration. Forms come from the engine's descriptors.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { Integration, KindDescriptor, Port } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import IntegrationForm from '$lib/IntegrationForm.svelte';
	import IntegrationCard from '$lib/system/IntegrationCard.svelte';
	import AddCard from '$lib/system/AddCard.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import { I } from '$lib/ui/icons';
	import { materialize, ms, spring } from '$lib/ui/motion';
	import { flip } from 'svelte/animate';

	// Model auth is harness-owned (the harness's auth_json carries model
	// credentials), so there is no "model provider" group.
	const GROUPS: { port: Port; title: string; sub: string }[] = $derived([
		{ port: 'forge', title: t('page.integrations.forge'), sub: t('page.integrations.forgeSub') },
		{
			port: 'provisioner',
			title: t('page.integrations.provisioner'),
			sub: t('page.integrations.provisionerSub')
		},
		{
			port: 'harness',
			title: t('page.integrations.harness'),
			sub: t('page.integrations.harnessSub')
		},
		{
			port: 'ticketsource',
			title: t('page.integrations.ticketsource'),
			sub: t('page.integrations.ticketsourceSub')
		}
	]);

	let descriptors = $state<KindDescriptor[]>([]);
	let integrations = $state<Integration[]>([]);
	let loaded = $state(false);
	let error = $state('');
	/** port → kind being added */
	let adding = $state<Partial<Record<Port, string>>>({});

	const refresh = async () => {
		try {
			[descriptors, integrations] = await Promise.all([
				api.system.kinds(),
				api.integrations.list()
			]);
			loaded = true;
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	};
	onMount(refresh);

	const kindsFor = (port: Port) => descriptors.filter((d) => d.port === port);
	const rowsFor = (port: Port) => integrations.filter((i) => i.port === port);
	const descriptorOf = (i: Integration) =>
		descriptors.find((d) => d.port === i.port && d.kind === i.kind);
	// Cards make room on the spring when one arrives or leaves.
	const move = () => ({ duration: ms('--dur-spring', 450), easing: spring });
	const usedBy = (i: Integration) =>
		fleet.projects
			.filter(
				(p) => p.forgeId === i.id || p.provisionerId === i.id || (p.harnessIds ?? []).includes(i.id)
			)
			.map((p) => p.name);
</script>

{#if error && !loaded}
	<ErrorCard cause={t('page.integrations.loadError')} detail={error} onretry={refresh} />
{:else if !loaded}
	{#each GROUPS as g (g.port)}
		<section class="col">
			<SectionHeader title={g.title} />
			<div class="card-grid">
				<Skeleton pane lines={3} />
				<Skeleton pane lines={3} />
			</div>
		</section>
	{/each}
{:else if descriptors.length === 0}
	<EmptyState title={t('page.integrations.noKinds')} text={t('page.integrations.noKindsText')} />
{:else}
	{#if error}<ErrorCard
			cause={t('page.integrations.refreshError')}
			detail={error}
			onretry={refresh}
		/>{/if}
	{#each GROUPS as g (g.port)}
		{@const rows = rowsFor(g.port)}
		{@const kinds = kindsFor(g.port)}
		<section class="col">
			<SectionHeader title={g.title} count={rows.length} sub={g.sub}>
				{#snippet actions()}
					{#each kinds as d (d.kind)}
						<button
							class="btn-sm"
							class:btn-sel={adding[g.port] === d.kind}
							onclick={() =>
								(adding = {
									...adding,
									[g.port]: adding[g.port] === d.kind ? undefined : d.kind
								})}>{I.add} {t('page.integrations.add', { label: d.label })}</button
						>
					{/each}
				{/snippet}
			</SectionHeader>

			{#if adding[g.port]}
				{@const d = kinds.find((k) => k.kind === adding[g.port])}
				{#if d}
					<AddCard
						title={t('page.integrations.new', { label: d.label })}
						kind={g.title.toLowerCase()}
					>
						{#key d.kind}
							<IntegrationForm
								descriptor={d}
								onsaved={() => {
									adding = { ...adding, [g.port]: undefined };
									void refresh();
									void fleet.refresh();
								}}
								oncancel={() => (adding = { ...adding, [g.port]: undefined })}
							/>
						{/key}
					</AddCard>
				{/if}
			{/if}

			{#if rows.length}
				<div class="card-grid">
					{#each rows as i (i.id)}
						<div class="cell" animate:flip={move()} in:materialize={{ y: 8 }}>
							<IntegrationCard
								integration={i}
								descriptor={descriptorOf(i)}
								usedBy={usedBy(i)}
								onchanged={() => {
									void refresh();
									void fleet.refresh();
								}}
							/>
						</div>
					{/each}
				</div>
			{:else if !adding[g.port]}
				<EmptyState
					inline
					title={t('page.integrations.none')}
					text={g.port === 'ticketsource'
						? t('page.integrations.optional')
						: t('page.integrations.needsOne', { kind: g.title.toLowerCase() })}
				/>
			{/if}
		</section>
	{/each}
{/if}

<style>
	.card-grid {
		--min: 380px;
	}
	.cell {
		min-width: 0;
	}
</style>
