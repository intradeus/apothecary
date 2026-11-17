<script lang="ts">
	// One kind of integration (DESIGN 7.10) — repositories, ticket sources or
	// harnesses: its configured integrations as cards, and the button that adds
	// one, a menu when the port has several kinds. Forms come from the engine's
	// descriptors.
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { Integration, KindDescriptor } from '$lib/domain';
	import type { IntegrationPort } from '$lib/core/urls';
	import { fleet } from '$lib/app/fleet.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import IntegrationForm from './IntegrationForm.svelte';
	import IntegrationCard from './IntegrationCard.svelte';
	import AddCard from './AddCard.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ActionMenu from '$lib/ui/ActionMenu.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import { materialize, ms, spring } from '$lib/ui/motion';
	import { flip } from 'svelte/animate';

	let { port }: { port: IntegrationPort } = $props();

	const title = $derived(t(`page.integrations.${port}`));
	const one = $derived(t(`page.integrations.${port}One`));

	let descriptors = $state<KindDescriptor[]>([]);
	let integrations = $state<Integration[]>([]);
	let credentialNames = $state<Record<string, string>>({});
	let loaded = $state(false);
	let error = $state('');
	/** The kind being added, if any. */
	let adding = $state<string | null>(null);

	const refresh = async () => {
		try {
			let creds;
			let all;
			[descriptors, all, creds] = await Promise.all([
				api.system.kinds(),
				api.integrations.list(),
				api.credentials.list().catch(() => [])
			]);
			integrations = all.filter((i) => i.port === port);
			credentialNames = Object.fromEntries(creds.map((c) => [c.id, c.name]));
			loaded = true;
			error = '';
		} catch (e) {
			error = errorMessage(e);
		}
	};
	onMount(refresh);

	const kinds = $derived(descriptors.filter((d) => d.port === port));
	const addingKind = $derived(kinds.find((k) => k.kind === adding));
	const descriptorOf = (i: Integration) => kinds.find((d) => d.kind === i.kind);
	const changed = () => {
		void refresh();
		void fleet.refresh();
	};
	// Cards make room on the spring when one arrives or leaves.
	const move = () => ({ duration: ms('--dur-spring', 450), easing: spring });
	const usedBy = (i: Integration) =>
		fleet.projects
			.filter(
				(p) =>
					p.repoAccountId === i.id ||
					p.ticketSourceId === i.id ||
					(p.harnessIds ?? []).includes(i.id)
			)
			.map((p) => p.name);
</script>

{#if error && !loaded}
	<ErrorCard cause={t('page.integrations.loadError')} detail={error} onretry={refresh} />
{:else if !loaded}
	<section class="col">
		<SectionHeader {title} />
		<div class="card-grid">
			<Skeleton pane lines={3} />
			<Skeleton pane lines={3} />
		</div>
	</section>
{:else if kinds.length === 0}
	<EmptyState title={t('page.integrations.noKinds')} text={t('page.integrations.noKindsText')} />
{:else}
	{#if error}<ErrorCard
			cause={t('page.integrations.refreshError')}
			detail={error}
			onretry={refresh}
		/>{/if}
	<section class="col">
		<SectionHeader {title} count={integrations.length}>
			{#snippet actions()}
				{#if kinds.length === 1}
					<button class="btn-primary" onclick={() => (adding = kinds[0].kind)}
						>{I.add} {t('page.integrations.add', { kind: one })}</button
					>
				{:else}
					<ActionMenu
						label={t('page.integrations.add', { kind: one })}
						groups={[
							{ items: kinds.map((d) => ({ label: d.label, run: () => (adding = d.kind) })) }
						]}
					/>
				{/if}
			{/snippet}
		</SectionHeader>

		{#if addingKind}
			<AddCard title={t('page.integrations.new', { label: addingKind.label })} kind={one}>
				{#key addingKind.kind}
					<IntegrationForm
						descriptor={addingKind}
						onsaved={() => {
							adding = null;
							changed();
						}}
						oncancel={() => (adding = null)}
					/>
				{/key}
			</AddCard>
		{/if}

		{#if integrations.length}
			<div class="card-grid">
				{#each integrations as i (i.id)}
					<div class="cell" animate:flip={move()} in:materialize={{ y: 8 }}>
						<IntegrationCard
							integration={i}
							descriptor={descriptorOf(i)}
							usedBy={usedBy(i)}
							{credentialNames}
							onchanged={changed}
						/>
					</div>
				{/each}
			</div>
		{:else if !addingKind}
			<EmptyState
				inline
				title={t('page.integrations.none')}
				text={port === 'ticketsource'
					? t('page.integrations.optional')
					: t('page.integrations.needsOne', { kind: one })}
			/>
		{/if}
	</section>
{/if}

<style>
	.card-grid {
		--min: 380px;
	}
	.cell {
		min-width: 0;
	}
</style>
