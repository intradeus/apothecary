<script lang="ts">
	// System notices: setup problems the signed-in user must act on (a login
	// to sign in again, a failing channel, a tracker that grew statuses). Each
	// stays until dismissed. They come from the shared fleet poll.
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { errorMessage } from '$lib/core/errors';
	import { ago } from '$lib/core/format';
	import { t } from '$lib/i18n/index.svelte';
	import type { Notice } from '$lib/domain';
	import Banner from '$lib/ui/Banner.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';

	let error = $state('');

	async function act(call: () => Promise<void>) {
		try {
			await call();
			error = '';
			await fleet.refresh();
		} catch (e) {
			error = errorMessage(e);
		}
	}

	const aside = (n: Notice) => ago(n.lastSeenAt) + (n.count > 1 ? ` · ×${n.count}` : '');
</script>

<PageHeader title={t('page.notices.title')}>
	{#snippet actions()}
		{#if fleet.notices.length}
			<button class="btn-sm" onclick={() => act(() => api.notices.dismissAll())}
				>{t('notices.dismissAll')}</button
			>
		{/if}
	{/snippet}
</PageHeader>

<!-- The page window every list page has (DESIGN 7): the notices sit on its
     glass, each banner falling back to an inset by itself. -->
<div class="page glass">
	{#if error}<ErrorCard cause={error} ondismiss={() => (error = '')} />{/if}
	{#if fleet.updatedAt === null}
		<Skeleton pane lines={4} />
	{:else if !fleet.notices.length}
		<EmptyState title={t('notices.empty')} />
	{:else}
		<div class="list">
			{#each fleet.notices as n (n.id)}
				<Banner
					tone="attention"
					title={n.title}
					aside={aside(n)}
					action={n.link ? t('notices.open') : undefined}
					onaction={n.link ? () => goto(n.link) : undefined}
					ondismiss={() => act(() => api.notices.dismiss(n.id))}
				>
					{#if n.body}<p class="body">{n.body}</p>{/if}
				</Banner>
			{/each}
		</div>
	{/if}
</div>

<style>
	.list {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.body {
		margin: 0;
		white-space: pre-line;
		flex-basis: 100%;
	}
</style>
