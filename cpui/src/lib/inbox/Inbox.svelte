<script lang="ts">
	// The inbox (DESIGN 7.5): a list of runs waiting on you (one card per run,
	// its newest pending ping first — answering it reveals the next-earlier
	// one), the selected ping at full size beside it, and the answered history.
	//
	// Pending pings come from the shared fleet poll; the answered history is
	// fetched here. Selection mirrors ?ping= (shallow, no navigation) so a
	// selected ping is a deep link.
	import { onMount } from 'svelte';
	import { replaceState } from '$app/navigation';
	import { currentGuild, to } from '$lib/guild';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { ago, age, fmtAbs, fmtCost, shortId } from '$lib/format';
	import { lore, isLore } from '$lib/lore';
	import { pingHeadline } from '$lib/ping';
	import type { Ping, Ticket } from '$lib/domain';
	import PingCard from '$lib/PingCard.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import PingPill, { pingMeta } from '$lib/ui/PingPill.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import StageFlask from '$lib/ui/StageFlask.svelte';
	import WorkspaceStatus from '$lib/ui/WorkspaceStatus.svelte';
	import Vial from '$lib/ui/Vial.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { runFacts, stageStatus } from '$lib/desk-home/runfacts.svelte';

	let {
		initial = '',
		initialTab = 'pending'
	}: { initial?: string; initialTab?: 'pending' | 'answered' } = $props();

	// svelte-ignore state_referenced_locally
	let tab = $state<'pending' | 'answered'>(initialTab);
	let typeFilter = $state('');
	// svelte-ignore state_referenced_locally
	let selected = $state(initial);
	let busy = $state<Record<string, boolean>>({});
	let error = $state('');

	let answered = $state<Ping[]>([]);
	let answeredLoaded = $state(false);
	let answeredErr = $state('');
	let tickets = $state<Map<string, Ticket>>(new Map());

	async function loadAnswered() {
		try {
			answered = await api.pings.list('answered');
			answeredErr = '';
		} catch (e) {
			answeredErr = e instanceof Error ? e.message : String(e);
		} finally {
			answeredLoaded = true;
		}
	}

	onMount(() => {
		api.tickets
			.list()
			.then((ts) => (tickets = new Map(ts.map((t) => [t.id, t]))))
			.catch(() => {});
		const t = setInterval(() => {
			if (tab === 'answered' && !document.hidden) void loadAnswered();
		}, 8000);
		return () => clearInterval(t);
	});
	$effect(() => {
		if (tab === 'answered') void loadAnswered();
	});

	// One group per run: its pending pings newest first (the one to answer is
	// [0]); groups in the order their shown ping was raised, newest first.
	const groups = $derived.by(() => {
		const byRun = new Map<string, Ping[]>();
		for (const p of fleet.pending) byRun.set(p.runId, [...(byRun.get(p.runId) ?? []), p]);
		return [...byRun.values()]
			.map((g) => [...g].sort((a, b) => b.raisedAt.localeCompare(a.raisedAt)))
			.sort((a, b) => b[0].raisedAt.localeCompare(a[0].raisedAt));
	});
	const types = $derived.by(() => {
		const m = new Map<string, number>();
		for (const g of groups) m.set(g[0].type, (m.get(g[0].type) ?? 0) + 1);
		return [...m.entries()];
	});
	const shownGroups = $derived(
		typeFilter ? groups.filter((g) => g[0].type === typeFilter) : groups
	);

	// The selected ping: an explicit pick if it is still pending, else the
	// first card. A ping that was just answered falls through to its run's
	// next-earlier one, which now heads the same group.
	let lastRun = $state('');
	const current = $derived.by(() => {
		const all = groups.flat();
		const exact = all.find((p) => p.id === selected);
		if (exact) {
			// A queued (older) ping was linked: show its run's head instead —
			// the newest is the one to answer first.
			return groups.find((g) => g[0].runId === exact.runId)?.[0] ?? exact;
		}
		const sameRun = groups.find((g) => g[0].runId === lastRun);
		return sameRun?.[0] ?? shownGroups[0]?.[0] ?? null;
	});
	const currentGroup = $derived(current ? (groups.find((g) => g[0].id === current.id) ?? []) : []);
	$effect(() => {
		if (current) lastRun = current.runId;
	});

	function pick(p: Ping) {
		selected = p.id;
		lastRun = p.runId;
		try {
			replaceState(`${to.inbox()}?ping=${p.id}`, {});
		} catch {
			/* router not ready — the selection still holds */
		}
	}

	async function answer(p: Ping, ans: string, note?: string, artifactIds?: string[]) {
		busy[p.id] = true;
		error = '';
		try {
			await api.pings.answer(p.id, ans, note?.trim() || undefined, artifactIds);
			// The deep link named the ping just answered: point it at the inbox.
			if (location.pathname.endsWith(p.id) || location.search.includes(p.id)) {
				try {
					replaceState(to.inbox(), {});
				} catch {
					/* router not ready */
				}
			}
			await fleet.refresh();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy[p.id] = false;
		}
	}

	const run = $derived(current ? fleet.runById.get(current.runId) : undefined);
	const facts = $derived(run ? runFacts(run) : undefined);
	const ticketOf = (runId: string) => {
		const tid = fleet.runById.get(runId)?.ticketId;
		return tid ? tickets.get(tid) : undefined;
	};
	const workshopName = (runId: string) => {
		const pid = fleet.runById.get(runId)?.projectId;
		return pid ? fleet.projectById.get(pid)?.name : undefined;
	};
</script>

<PageHeader
	crumbs={[{ label: currentGuild().name, href: to.desk() }, { label: 'Inbox' }]}
	title="Inbox"
	accent={isLore() ? lore('needsYou').toLowerCase() : undefined}
>
	{#snippet meta()}
		<span>
			{fleet.pending.length
				? `${fleet.pending.length} pending across ${groups.length} run${groups.length === 1 ? '' : 's'}`
				: 'nothing pending'}
		</span>
	{/snippet}
	{#snippet actions()}
		<Segmented
			bind:value={tab}
			options={[
				{ value: 'pending', label: 'Pending', count: fleet.pending.length },
				{
					value: 'answered',
					label: 'Answered',
					count: answeredLoaded ? answered.length : undefined
				}
			]}
		/>
	{/snippet}
</PageHeader>

{#if tab === 'pending'}
	<div class="ib">
		{#if !fleet.updatedAt && fleet.error}
			<div class="full">
				<ErrorCard
					cause="Could not load pings"
					detail={fleet.error}
					onretry={() => fleet.refresh()}
				/>
			</div>
		{:else if !fleet.updatedAt}
			<div class="list c"><div class="pad"><Skeleton lines={5} /></div></div>
			<div class="detail"><div class="c sk"><Skeleton lines={4} /></div></div>
		{:else if groups.length === 0}
			<div class="full">
				<EmptyState
					title={isLore() ? 'All quiet at the bench' : 'Nothing needs you'}
					text="No pending pings. When a run asks you something, it lands here."
				>
					<a class="btn" href={to.runs()}>Open runs</a>
				</EmptyState>
			</div>
		{:else}
			<aside class="list c">
				{#if types.length > 1}
					<div class="chips">
						<button class="chip-f" class:on={!typeFilter} onclick={() => (typeFilter = '')}
							>All <span class="n">{groups.length}</span></button
						>
						{#each types as [t, n] (t)}
							<button
								class="chip-f h-{pingMeta(t).hue}"
								class:on={typeFilter === t}
								onclick={() => (typeFilter = typeFilter === t ? '' : t)}
								><span class="d"></span>{pingMeta(t).label} <span class="n">{n}</span></button
							>
						{/each}
					</div>
				{/if}
				<div class="scroll">
					{#each shownGroups as g (g[0].runId)}
						{@const p = g[0]}
						{@const r = fleet.runById.get(p.runId)}
						{@const tk = ticketOf(p.runId)}
						<button class="card" class:sel={current?.id === p.id} onclick={() => pick(p)}>
							<span class="l1">
								<PingPill type={p.type} />
								{#if p.stageId}<span class="mono st ell">{p.stageId}</span>{/if}
								<span class="spacer"></span>
								<span class="mono age" title={fmtAbs(p.raisedAt)}>{age(p.raisedAt)}</span>
							</span>
							<span class="q ell">{pingHeadline(p.question)}</span>
							<span class="l3">
								<span class="rt ell">{r?.title ?? shortId(p.runId)}</span>
								{#if tk?.source?.key}<span class="tag">{tk.source.key}</span>{/if}
								{#if r?.projectId}
									<span class="wsn"
										><Vial projectId={r.projectId} /><span class="ell"
											>{workshopName(p.runId) ?? ''}</span
										></span
									>
								{/if}
							</span>
							{#if g.length > 1}
								<span class="queued"
									>Answer this first — {g.length - 1} earlier ping{g.length > 2 ? 's' : ''} queued</span
								>
							{/if}
						</button>
					{:else}
						<div class="pad mut small">No {pingMeta(typeFilter).label.toLowerCase()} pings.</div>
					{/each}
				</div>
			</aside>

			<section class="detail">
				{#if current}
					{#if run}
						<div class="strip c">
							<StatusChip state={run.state} size="sm" />
							{#if run.currentStage}
								<span class="stg">
									<StageFlask status={stageStatus(run.state)} size="sm" />
									<span class="mono">{run.currentStage}</span>
									{#if facts?.attempt}<span class="fnt">· attempt {facts.attempt}</span>{/if}
								</span>
							{/if}
							{#if facts && facts.cost > 0}<span class="mono mut">{fmtCost(facts.cost)}</span>{/if}
							<span class="wsx"
								><WorkspaceStatus
									state={run.state}
									envId={run.envId}
									envStatus={run.envStatus}
								/></span
							>
							<span class="spacer"></span>
							{#if run.mrUrl}<a class="mr" href={run.mrUrl} target="_blank" rel="noreferrer">MR ↗</a
								>{/if}
							<a class="btn btn-sm" href={to.run(run.id)}>Open run →</a>
						</div>
					{/if}
					{#if error}<ErrorCard cause="The answer did not go through" detail={error} />{/if}
					{#key current.id}
						<PingCard
							ping={current}
							busy={busy[current.id] ?? false}
							onanswer={(a, n, ids) => answer(current, a, n, ids)}
							runTitle={run?.title}
							ticketKey={ticketOf(current.runId)?.source?.key}
							ticketTitle={ticketOf(current.runId)?.title}
						/>
					{/key}
					{#if currentGroup.length > 1}
						<div class="later">
							<div class="lh">Queued behind this one</div>
							{#each currentGroup.slice(1) as q (q.id)}
								<div class="lr">
									<PingPill type={q.type} />
									<span class="ell">{pingHeadline(q.question)}</span>
									<span class="mono fnt" title={fmtAbs(q.raisedAt)}>{ago(q.raisedAt)}</span>
								</div>
							{/each}
						</div>
					{/if}
				{/if}
			</section>
		{/if}
	</div>
{:else}
	<div class="page hist">
		{#if answeredErr && !answered.length}
			<ErrorCard cause="Could not load the history" detail={answeredErr} onretry={loadAnswered} />
		{:else if !answeredLoaded}
			<div class="c pane sk"><Skeleton lines={6} /></div>
		{:else if answered.length === 0}
			<EmptyState
				compact
				title="No answers yet"
				text="Pings you answer are kept here, newest first."
			/>
		{:else}
			<div class="c pane">
				<div class="rows">
					<div class="r head ah">
						<span>Type</span><span>Question</span><span class="wide">Run</span><span class="wide"
							>Answer</span
						><span class="right">Answered</span>
					</div>
					{#each answered as p (p.id)}
						<div class="r ah">
							<span><PingPill type={p.type} /></span>
							<a class="q ell stretch" href={to.ping(p.id)}>{pingHeadline(p.question)}</a>
							<span class="ell mut wide"
								>{fleet.runById.get(p.runId)?.title ?? shortId(p.runId)}{#if p.stageId}<span
										class="mono fnt">{' · '}{p.stageId}</span
									>{/if}</span
							>
							<span
								class="ans ell wide"
								class:ok={p.answer === 'approve'}
								class:bad={p.answer === 'reject'}
								>{p.answer ?? '—'}{#if p.feedback}<span class="fnt">{' · '}{p.feedback}</span
									>{/if}</span
							>
							<span class="mono mut right" title={p.answeredAt ? fmtAbs(p.answeredAt) : ''}
								>{p.answeredAt ? ago(p.answeredAt) : ''}</span
							>
						</div>
					{/each}
				</div>
			</div>
		{/if}
	</div>
{/if}

<style>
	.ib {
		flex: 1;
		min-height: 0;
		display: grid;
		grid-template-columns: 380px minmax(0, 1fr);
		gap: 12px;
		padding: 2px 22px 16px;
	}
	.full {
		grid-column: 1 / -1;
	}
	.list {
		border-radius: 16px;
		overflow: hidden;
		display: flex;
		flex-direction: column;
		min-height: 0;
	}
	.pad {
		padding: 14px 16px;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 5px;
		padding: 10px 12px;
		border-bottom: 1px solid var(--cLn);
	}
	.chip-f {
		font-size: 11.5px;
		padding: 2px 10px;
		border-radius: 999px;
		background: transparent;
		border: 1px solid var(--ln);
		box-shadow: none;
		color: var(--mut);
		gap: 5px;
	}
	.chip-f .d {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--h, var(--mut));
	}
	.chip-f .n {
		font-family: var(--mono);
		font-size: 10px;
	}
	.chip-f.on {
		background: var(--sel);
		box-shadow:
			inset 0 0 0 1px var(--selLn),
			var(--selSh);
		color: var(--tx);
		font-weight: 600;
		border-color: transparent;
	}
	.h-grn {
		--h: var(--grn);
	}
	.h-amb {
		--h: var(--amb);
	}
	.h-ros {
		--h: var(--ros);
	}
	.h-vio {
		--h: var(--vio);
	}
	.h-tea {
		--h: var(--tea);
	}
	.scroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
	}
	.card {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		width: 100%;
		display: flex;
		flex-direction: column;
		gap: 5px;
		padding: 11px 14px 12px;
		border-bottom: 1px solid var(--cLn);
		position: relative;
	}
	.card:hover {
		background: var(--p1);
	}
	.card.sel {
		background: var(--p3);
	}
	.card.sel::before {
		content: '';
		position: absolute;
		left: 0;
		top: 10px;
		bottom: 10px;
		width: 3px;
		border-radius: 0 3px 3px 0;
		background: var(--amb);
		box-shadow: 0 0 8px var(--amb);
	}
	.card:focus-visible {
		outline: 2px solid var(--brand);
		outline-offset: -2px;
	}
	.l1,
	.l3 {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.st {
		font-size: 11px;
		color: var(--mut);
	}
	.age {
		font-size: 10.5px;
		color: var(--mut);
		flex: none;
	}
	.spacer {
		flex: 1;
	}
	.q {
		font-weight: 500;
		font-size: 12.5px;
		color: var(--tx);
	}
	.l3 {
		font-size: 11.5px;
		color: var(--mut);
	}
	.rt {
		flex: 0 1 auto;
		color: var(--tx);
		opacity: 0.85;
	}
	.wsn {
		display: flex;
		align-items: center;
		gap: 5px;
		min-width: 0;
		flex: 0 1 auto;
	}
	.queued {
		font-size: 11.5px;
		color: var(--amb);
	}
	.detail {
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 2px 2px 8px;
	}
	.strip {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 7px 8px 7px 12px;
		border-radius: 14px;
		font-size: 12px;
		flex: none;
	}
	.wsx {
		flex: 0 1 auto;
		min-width: 0;
		overflow: hidden;
		white-space: nowrap;
		display: flex;
	}
	.stg {
		flex: none;
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	.stg .mono {
		font-size: 11.5px;
	}
	.strip > .btn,
	.mr {
		flex: none;
		white-space: nowrap;
	}
	.mr {
		color: var(--brand);
		font-weight: 600;
		font-size: 12px;
	}
	.later {
		border: 1px dashed var(--ln2);
		border-radius: 14px;
		padding: 8px 12px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		flex: none;
	}
	.lh {
		font-size: 11px;
		color: var(--fnt);
	}
	.lr {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		gap: 10px;
		align-items: center;
		font-size: 12px;
		color: var(--mut);
	}
	.lr .mono {
		font-size: 10.5px;
	}
	.sk {
		border-radius: 16px;
		padding: 16px;
	}
	.hist {
		padding-top: 2px;
	}
	.pane {
		border-radius: 16px;
		overflow: hidden;
		flex: none;
	}
	.ah {
		grid-template-columns: 120px minmax(0, 2fr) minmax(0, 1.2fr) minmax(0, 1fr) 80px;
		position: relative;
		font-size: 12.5px;
	}
	.ah .q:hover {
		text-decoration: none;
	}
	.stretch::after {
		content: '';
		position: absolute;
		inset: 0;
	}
	.ans {
		font-weight: 600;
	}
	.ans.ok {
		color: var(--grn);
	}
	.ans.bad {
		color: var(--ros);
	}
	.ans .fnt {
		font-weight: 400;
	}
	.right {
		text-align: right;
		font-size: 11px;
	}
	@media (max-width: 900px) {
		.ib {
			grid-template-columns: minmax(0, 1fr);
			padding: 2px 16px 16px;
			overflow: auto;
		}
		.list {
			max-height: 45vh;
		}
		.ib {
			align-content: start;
		}
		.strip {
			flex-wrap: wrap;
		}
		.detail {
			overflow: visible;
		}
		.ah {
			grid-template-columns: 110px minmax(0, 1fr) 70px;
		}
		.ah .wide {
			display: none;
		}
	}
</style>
