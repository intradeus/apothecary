<script lang="ts">
	// Model providers (docs/model-providers-plan.md): one's API keys and
	// logins, which harness integrations reference. Add an API key, sign in
	// from the engine, or import a file a CLI wrote; each card shows what the
	// engine keeps: the account, how long the token is good for, when it was
	// last refreshed — never the key or the token.
	import { onMount } from 'svelte';
	import { flip } from 'svelte/animate';
	import { api } from '$lib/api';
	import type { Credential, Integration, ProviderInfo } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import AddCard from '$lib/system/AddCard.svelte';
	import AddCredential from '$lib/system/AddCredential.svelte';
	import CredentialCard from '$lib/system/CredentialCard.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ActionMenu from '$lib/ui/ActionMenu.svelte';
	import HeaderAction from '$lib/ui/HeaderAction.svelte';
	import { materialize, ms, spring } from '$lib/ui/motion';

	type Mode = 'key' | 'signin' | 'import';

	let providers = $state<ProviderInfo[]>([]);
	let creds = $state<Credential[]>([]);
	let harnesses = $state<Integration[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let adding = $state<Mode | null>(null);

	const refresh = async () => {
		try {
			const [p, c, ints] = await Promise.all([
				api.credentials.providers(),
				api.credentials.list(),
				api.integrations.list()
			]);
			providers = p;
			creds = c;
			harnesses = ints.filter((i) => i.port === 'harness');
			loaded = true;
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	};
	onMount(refresh);

	const providerOf = (c: Credential) => providers.find((p) => p.id === c.provider);
	const usedBy = (c: Credential) =>
		harnesses
			.filter((h) => {
				const ids = h.config['credential_ids'];
				return Array.isArray(ids) && ids.includes(c.id);
			})
			.map((h) => h.name);
	const move = () => ({ duration: ms('--dur-spring', 450), easing: spring });
	const modes: { id: Mode; label: string }[] = $derived([
		{ id: 'key', label: t('providers.addKey') },
		{ id: 'signin', label: t('providers.addSignIn') },
		{ id: 'import', label: t('providers.addImport') }
	]);
	const menu = $derived([
		{ items: modes.map((m) => ({ label: m.label, run: () => (adding = m.id) })) }
	]);
</script>

{#if error && !loaded}
	<ErrorCard cause={t('providers.loadError')} detail={error} onretry={refresh} />
{:else if !loaded}
	<section class="col">
		<SectionHeader title={t('providers.section')} />
		<div class="card-grid">
			<Skeleton pane lines={3} />
			<Skeleton pane lines={3} />
		</div>
	</section>
{:else}
	<HeaderAction>
		<ActionMenu label={t('providers.new')} groups={menu} />
	</HeaderAction>
	<section class="col">
		<SectionHeader
			title={t('providers.section')}
			count={creds.length}
			sub={t('providers.sectionSub')}
		/>

		{#if adding}
			{@const label = modes.find((m) => m.id === adding)?.label ?? ''}
			<AddCard title={label}>
				{#key adding}
					<AddCredential
						mode={adding}
						{providers}
						onadded={(_, done) => {
							if (done) adding = null;
							void refresh();
						}}
						oncancel={() => (adding = null)}
					/>
				{/key}
			</AddCard>
		{/if}

		{#if creds.length}
			<div class="card-grid">
				{#each creds as c (c.id)}
					<div class="cell" animate:flip={move()} in:materialize={{ y: 8 }}>
						<CredentialCard
							credential={c}
							provider={providerOf(c)}
							usedBy={usedBy(c)}
							onchanged={refresh}
						/>
					</div>
				{/each}
			</div>
		{:else if !adding}
			<EmptyState inline title={t('providers.none')} text={t('providers.noneText')} />
		{/if}
	</section>
{/if}

<style>
	.card-grid {
		--min: 340px;
	}
	.cell {
		min-width: 0;
	}
</style>
