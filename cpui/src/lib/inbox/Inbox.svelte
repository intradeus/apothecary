<script lang="ts">
	// The inbox (DESIGN 7.5): a list of runs waiting on you (one card per run,
	// its newest pending ping first — answering it reveals the next-earlier
	// one), the selected ping at full size beside it, and the answered history.
	//
	// Pending pings come from the shared fleet poll; the answered history is
	// fetched here. Selection mirrors ?ping= (shallow, no navigation) so a
	// selected ping is a deep link.
	import { onMount } from 'svelte';
	import { flip } from 'svelte/animate';
	import { goto, replaceState } from '$app/navigation';
	import { currentGuild, to } from '$lib/guild';
	import { api } from '$lib/api';
	import { fleet } from '$lib/fleet.svelte';
	import { ago, age, fmtAbs, fmtCost, shortId } from '$lib/format';
	import { answerMeta, pingHeadline } from '$lib/ping';
	import type { Ping, Ticket } from '$lib/domain';
	import PingCard from '$lib/PingCard.svelte';
	import PageHeader from '$lib/ui/PageHeader.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import PingPill, { pingMeta } from '$lib/ui/PingPill.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import StageFlask from '$lib/ui/StageFlask.svelte';
	import WorkspaceStatus from '$lib/ui/WorkspaceStatus.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';
	import type { Hue } from '$lib/ui/hues';
	import { indicator, ms, slide, spring } from '$lib/ui/motion';
	import WorkshopName from '$lib/workshop/WorkshopName.svelte';
	import MrLink from '$lib/run/MrLink.svelte';
	import { runFacts, stageStatus } from '$lib/run/runfacts.svelte';

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
	const typeOptions = $derived([
		{ value: '', label: 'All', count: groups.length },
		...types.map(([t, n]) => ({
			value: t,
			label: pingMeta(t).label,
			count: n,
			dot: pingMeta(t).hue as Hue
		}))
	]);

	// The selected ping: an explicit pick if it is still pending, else the
	// first card. A ping that was just answered falls through to its run's
	// next-earlier one, which now heads the same group.
	// When a run's last ping is answered its card leaves; the one that slid
	// into its place (the next card down) is selected, not the top of the list.
	let lastRun = $state('');
	let lastIdx = $state(0);
	const current = $derived.by(() => {
		const all = groups.flat();
		const exact = all.find((p) => p.id === selected);
		if (exact) {
			// A queued (older) ping was linked: show its run's head instead —
			// the newest is the one to answer first.
			return groups.find((g) => g[0].runId === exact.runId)?.[0] ?? exact;
		}
		const sameRun = groups.find((g) => g[0].runId === lastRun);
		return (
			sameRun?.[0] ??
			shownGroups[Math.min(lastIdx, shownGroups.length - 1)]?.[0] ??
			shownGroups[0]?.[0] ??
			null
		);
	});
	const currentGroup = $derived(current ? (groups.find((g) => g[0].id === current.id) ?? []) : []);
	$effect(() => {
		if (!current) return;
		lastRun = current.runId;
		const i = shownGroups.findIndex((g) => g[0].runId === current.runId);
		if (i >= 0) lastIdx = i;
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
			const runId = p.runId;
			toast('Answer sent', {
				action: { label: 'Open run', run: () => void goto(to.run(runId)) }
			});
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
	const move = { duration: ms('--dur-spring', 450), easing: spring };
</script>

<PageHeader
	crumbs={[{ label: currentGuild().name, href: to.desk() }, { label: 'Inbox' }]}
	title="Inbox"
	accent="awaiting your hand"
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
			label="Inbox"
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
			<Skeleton pane lines={5} />
			<Skeleton pane lines={4} />
		{:else if groups.length === 0}
			<div class="full">
				<EmptyState
					title="All quiet at the bench"
					text="No pending pings. When a run asks you something, it lands here."
				>
					<a class="btn" href={to.runs()}>Open runs</a>
				</EmptyState>
			</div>
		{:else}
			<aside class="list c pane">
				{#if types.length > 1}
					<div class="pane-head filt">
						<Segmented
							bind:value={typeFilter}
							options={typeOptions}
							size="sm"
							glass={false}
							label="Ping type"
						/>
					</div>
				{/if}
				<div class="scroll" use:indicator>
					{#each shownGroups as g, i (g[0].runId)}
						{@const p = g[0]}
						{@const r = fleet.runById.get(p.runId)}
						{@const tk = ticketOf(p.runId)}
						<button
							class="rowbtn card"
							class:on={current?.id === p.id}
							aria-current={current?.id === p.id ? 'true' : undefined}
							onclick={() => {
								lastIdx = i;
								pick(p);
							}}
							animate:flip={move}
							out:slide={{ from: 'left' }}
						>
							<span class="l1">
								<PingPill type={p.type} />
								{#if p.stageId}<span class="mono mut st ell">{p.stageId}</span>{/if}
								<span class="spacer"></span>
								<span class="mono mut age" title={fmtAbs(p.raisedAt)}>{age(p.raisedAt)}</span>
							</span>
							<span class="q ell">{pingHeadline(p.question)}</span>
							<span class="l3">
								<span class="rt ell">{r?.title ?? shortId(p.runId)}</span>
								{#if tk?.source?.key}<span class="tag">{tk.source.key}</span>{/if}
								{#if r?.projectId}<span class="wsn"><WorkshopName projectId={r.projectId} /></span
									>{/if}
							</span>
							{#if g.length > 1}
								<span class="queued"
									>Answer this first — {g.length - 1} earlier ping{g.length > 2 ? 's' : ''} queued</span
								>
							{/if}
						</button>
					{:else}
						<div class="empty-line">No {pingMeta(typeFilter).label.toLowerCase()} pings.</div>
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
							{#if run.mrs.length || run.mrUrl}<MrLink {run} />{/if}
							<a class="btn btn-sm" href={to.run(run.id)}>Open run {I.forward}</a>
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
						<div class="note later">
							<div class="lbl">Queued behind this one</div>
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
			<Skeleton pane lines={6} />
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
						<span>Type</span><span>Question</span><span class="hide-sm">Run</span><span
							class="hide-sm">Answer</span
						><span class="num">Answered</span>
					</div>
					{#each answered as p (p.id)}
						{@const v = answerMeta(p)}
						<div class="r ah">
							<span><PingPill type={p.type} /></span>
							<a class="q ell stretch" href={to.ping(p.id)}>{pingHeadline(p.question)}</a>
							<span class="ell mut hide-sm"
								>{fleet.runById.get(p.runId)?.title ?? shortId(p.runId)}{#if p.stageId}<span
										class="mono fnt">{' · '}{p.stageId}</span
									>{/if}</span
							>
							<span class="ans ell hide-sm"
								><span class="hue-{v.hue}">{v.glyph} {p.answer ?? '—'}</span>{#if p.feedback}<span
										class="fnt">{' · '}{p.feedback}</span
									>{/if}</span
							>
							<span class="mono mut num" title={p.answeredAt ? fmtAbs(p.answeredAt) : ''}
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
		grid-template-columns: var(--list-w) minmax(0, 1fr);
		grid-template-rows: minmax(0, 1fr);
		gap: 12px;
		padding: 2px 22px 16px;
	}
	.full {
		grid-column: 1 / -1;
	}
	.list {
		display: flex;
		flex-direction: column;
		min-height: 0;
	}
	.filt {
		padding: 8px 10px;
	}
	.scroll {
		flex: 1;
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 6px;
	}
	.scroll > :global(.ind) {
		border-radius: var(--r-card);
	}
	.card {
		flex-direction: column;
		align-items: stretch;
		gap: 5px;
		padding: 10px 12px 11px;
		border-radius: var(--r-card);
	}
	.l1,
	.l3 {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.st {
		font-size: var(--fs-meta);
	}
	.age {
		font-size: var(--fs-id);
		flex: none;
	}
	.q {
		font-weight: 500;
		font-size: var(--fs-ui);
		color: var(--tx);
	}
	.l3 {
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.rt {
		flex: 0 1 auto;
		color: var(--tx);
		opacity: 0.85;
	}
	.wsn {
		display: flex;
		min-width: 0;
		flex: 0 1 auto;
	}
	.queued {
		font-size: var(--fs-help);
		color: var(--amb);
	}
	.detail {
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 0 2px 8px;
	}
	.strip {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 7px 8px 7px 12px;
		border-radius: var(--r-card);
		font-size: var(--fs-label);
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
		font-size: var(--fs-help);
	}
	.strip > .btn {
		flex: none;
	}
	.later {
		display: flex;
		flex-direction: column;
		gap: 6px;
		flex: none;
	}
	.lr {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr) auto;
		gap: 10px;
		align-items: center;
	}
	.lr .mono {
		font-size: var(--fs-id);
	}
	.hist {
		padding-top: 2px;
	}
	.ah {
		grid-template-columns: 120px minmax(0, 2fr) minmax(0, 1.2fr) minmax(0, 1fr) 80px;
		font-size: var(--fs-ui);
	}
	.ah .q:hover {
		text-decoration: none;
	}
	.ans {
		font-weight: 600;
	}
	.ans .fnt {
		font-weight: 400;
	}
	.num {
		font-size: var(--fs-meta);
	}
	@media (max-width: 1000px) {
		.ib {
			grid-template-columns: minmax(0, 1fr);
			grid-template-rows: none;
			padding: 2px 16px 16px;
			overflow: auto;
			align-content: start;
		}
		.list {
			max-height: 45vh;
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
		.ah .hide-sm {
			display: none;
		}
	}
</style>
