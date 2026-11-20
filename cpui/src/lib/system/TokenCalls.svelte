<script lang="ts">
	// What one token did: its writes and its MCP tool calls, newest first,
	// refused or done. The acts themselves name the token's person; this
	// names the token.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { ago, fmtAbs } from '$lib/core/format';
	import type { TokenCall } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';

	let { tokenId }: { tokenId: string } = $props();

	let calls = $state<TokenCall[] | null>(null);
	let error = $state('');

	async function load() {
		error = '';
		try {
			calls = await api.auth.tokenCalls(tokenId);
		} catch (e) {
			error = errorMessage(e);
		}
	}
	onMount(load);
</script>

<div class="calls" aria-label={t('page.account.callsTitle')}>
	{#if error}
		<ErrorCard cause={t('page.account.callsFailed')} detail={error} onretry={load} />
	{:else if !calls}
		<Skeleton lines={2} />
	{:else if !calls.length}
		<p class="muted small">{t('page.account.noCalls')}</p>
	{:else}
		<ul class="list">
			{#each calls as c, i (i)}
				<li class="call">
					<span class="muted small" title={fmtAbs(c.at)}>{ago(c.at)}</span>
					<span class="mono small truncate" title={`${c.method} ${c.path}`}
						>{c.tool ?? `${c.method} ${c.path}`}</span
					>
					<span class="pill sm mono" class:tint-danger={c.status >= 400}>{c.status}</span>
				</li>
			{/each}
		</ul>
	{/if}
</div>

<style>
	.list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 4px;
		max-height: 240px;
		overflow-y: auto;
	}
	.call {
		display: grid;
		grid-template-columns: 90px minmax(0, 1fr) auto;
		gap: 10px;
		align-items: center;
	}
</style>
