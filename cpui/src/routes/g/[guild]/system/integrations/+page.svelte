<script lang="ts">
	// Integrations (DESIGN 7.10): four groups — forge, provisioner, harness,
	// ticket source — each with "+ Add <kind>" per registered kind and a card
	// per configured integration. Forms come from the engine's descriptors.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { Integration, KindDescriptor, Port } from '$lib/domain';
	import { fleet } from '$lib/fleet.svelte';
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
	const GROUPS: { port: Port; title: string; sub: string }[] = [
		{ port: 'forge', title: 'Forge', sub: 'code hosting: repositories, branches, merge requests' },
		{ port: 'provisioner', title: 'Provisioner', sub: 'where workspaces are built and run' },
		{
			port: 'harness',
			title: 'Harness',
			sub: 'the coding agent, and the model credentials it uses'
		},
		{
			port: 'ticketsource',
			title: 'Ticket source',
			sub: 'trackers to import tickets from (read-only)'
		}
	];

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
	<ErrorCard cause="Couldn’t load integrations" detail={error} onretry={refresh} />
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
	<EmptyState title="No kinds registered" text="This engine build reports no integration kinds." />
{:else}
	{#if error}<ErrorCard
			cause="Couldn’t refresh integrations"
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
								})}>{I.add} Add {d.label}</button
						>
					{/each}
				{/snippet}
			</SectionHeader>

			{#if adding[g.port]}
				{@const d = kinds.find((k) => k.kind === adding[g.port])}
				{#if d}
					<AddCard title="New {d.label}" kind={g.title.toLowerCase()}>
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
					title="None configured"
					text={g.port === 'ticketsource'
						? 'Optional — tickets can always be written by hand.'
						: `A workshop needs one ${g.title.toLowerCase()} to run.`}
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
