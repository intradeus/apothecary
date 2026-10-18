<script lang="ts">
	import { api } from '$lib/api';
	import { ago } from '$lib/format';
	import type { Ping } from '$lib/domain';
	import PingCard from '$lib/PingCard.svelte';
	import { isWantedPing } from '$lib/ping';
	import { onMount } from 'svelte';

	let pings = $state<Ping[]>([]);
	let loaded = $state(false);
	let tab = $state<'pending' | 'answered'>('pending');
	let busy = $state<Record<string, boolean>>({});
	let error = $state('');

	// Pending pings are grouped by run and shown one card per run: only the
	// LATEST unanswered ping of each run is displayed, so the operator answers
	// a run's pings newest-first, one after another (answering the shown ping
	// reveals the next-earlier one on the next refresh). `pings` already
	// arrives newest-first, so insertion order = latest-ping-first across runs.
	const runGroups = $derived.by(() => {
		const byRun = new Map<string, Ping[]>();
		for (const p of pings) {
			const arr = byRun.get(p.runId) ?? [];
			arr.push(p);
			byRun.set(p.runId, arr);
		}
		return [...byRun.values()];
	});

	const refresh = async () => {
		try {
			pings = await api.pings.list(tab);
			loaded = true;
		} catch {
			/* retry next tick */
		}
	};

	onMount(() => {
		const t = setInterval(refresh, 4000);
		return () => clearInterval(t);
	});
	$effect(() => {
		void tab;
		loaded = false;
		refresh();
	});

	async function answer(p: Ping, ans: string, note?: string, artifactIds?: string[]) {
		busy[p.id] = true;
		error = '';
		try {
			await api.pings.answer(p.id, ans, note?.trim() || undefined, artifactIds);
			await refresh();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy[p.id] = false;
		}
	}
</script>

<div class="row spread">
	<h1 style="margin:0">Inbox</h1>
	<div class="row" style="gap:0">
		<button class:primary={tab === 'pending'} onclick={() => (tab = 'pending')}>Pending</button>
		<button class:primary={tab === 'answered'} onclick={() => (tab = 'answered')}>Answered</button>
	</div>
</div>

{#if error}<div class="error">{error}</div>{/if}

{#snippet card(p: Ping, queued: number)}
	<!-- keyed by ping_id at the call site so answering rebuilds the card fresh
       (PingCard's draft note must not carry over to the next ping) -->
	<div class="card">
		<div class="row spread">
			<span class="pingtype {isWantedPing(p.type) ? 'wanted' : 'unwanted'}">{p.type}</span>
			<span class="muted small">
				{#if p.stageId}<span class="mono">{p.stageId}</span> ·
				{/if}
				{ago(p.raisedAt)} · <a href={`/runs/${p.runId}`}>view run</a>
			</span>
		</div>
		{#if queued > 0}
			<div class="queued small">
				answer this first — {queued} earlier ping{queued > 1 ? 's' : ''} queued for this run
			</div>
		{/if}
		<PingCard ping={p} busy={busy[p.id] ?? false} onanswer={(a, n, ids) => answer(p, a, n, ids)} />
	</div>
{/snippet}

{#if pings.length === 0}
	<div class="empty">
		{#if !loaded}Loading…{:else if tab === 'pending'}No pending pings — the apothecary needs nothing
			from you.{:else}No answered pings yet.{/if}
	</div>
{:else if tab === 'pending'}
	<!-- one card per run, showing only its latest pending ping -->
	{#each runGroups as g (g[0].id)}
		{@render card(g[0], g.length - 1)}
	{/each}
{:else}
	<!-- answered history: every ping, flat, newest-first -->
	{#each pings as p (p.id)}
		{@render card(p, 0)}
	{/each}
{/if}

<style>
	.queued {
		margin: 6px 0 2px;
		color: var(--amber);
		opacity: 0.85;
	}
</style>
