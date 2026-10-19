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
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';

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
		<section class="grp">
			<div class="gh"><h2>{g.title}</h2></div>
			<div class="cards">
				<div class="c sk"><Skeleton lines={3} /></div>
				<div class="c sk"><Skeleton lines={3} /></div>
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
		<section class="grp">
			<div class="gh">
				<h2>{g.title}</h2>
				<span class="count">{rows.length}</span>
				<span class="sub">{g.sub}</span>
				<span class="spacer"></span>
				{#each kinds as d (d.kind)}
					<button
						class="btn-sm"
						class:btn-sel={adding[g.port] === d.kind}
						onclick={() =>
							(adding = { ...adding, [g.port]: adding[g.port] === d.kind ? undefined : d.kind })}
						>+ Add {d.label}</button
					>
				{/each}
			</div>

			{#if adding[g.port]}
				{@const d = kinds.find((k) => k.kind === adding[g.port])}
				{#if d}
					<div class="c add">
						<div class="ah">
							<span class="serif at">New {d.label}</span>
							<span class="pill t-mut">{g.title.toLowerCase()}</span>
						</div>
						<IntegrationForm
							descriptor={d}
							onsaved={() => {
								adding = { ...adding, [g.port]: undefined };
								void refresh();
								void fleet.refresh();
							}}
							oncancel={() => (adding = { ...adding, [g.port]: undefined })}
						/>
					</div>
				{/if}
			{/if}

			{#if rows.length}
				<div class="cards">
					{#each rows as i (i.id)}
						<IntegrationCard
							integration={i}
							descriptor={descriptorOf(i)}
							usedBy={usedBy(i)}
							onchanged={() => {
								void refresh();
								void fleet.refresh();
							}}
						/>
					{/each}
				</div>
			{:else if !adding[g.port]}
				<div class="none">
					<span class="serif">None configured</span>
					<span class="mut"
						>{g.port === 'ticketsource'
							? 'Optional — tickets can always be written by hand.'
							: `A workshop needs one ${g.title.toLowerCase()} to run.`}</span
					>
				</div>
			{/if}
		</section>
	{/each}
{/if}

<style>
	.grp {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.gh {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		padding: 0 2px;
	}
	.gh h2 {
		font-size: 20px;
	}
	.sub {
		font-size: 12px;
		color: var(--mut);
	}
	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
		gap: 10px;
	}
	.sk {
		border-radius: 16px;
		padding: 14px;
	}
	.add {
		border-radius: 16px;
		padding: 14px 16px;
		display: flex;
		flex-direction: column;
		gap: 12px;
		border-color: var(--brandLn);
	}
	.ah {
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.at {
		font-size: 19px;
	}
	.none {
		border: 1.5px dashed var(--ln2);
		border-radius: 16px;
		padding: 12px 14px;
		display: flex;
		align-items: baseline;
		gap: 10px;
		font-size: 12px;
	}
	.none .serif {
		font-size: 16px;
	}
	@media (max-width: 720px) {
		.cards {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
