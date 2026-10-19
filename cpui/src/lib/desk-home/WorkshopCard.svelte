<script lang="ts">
	// A workshop on the desk (DESIGN 7.2/7.3): its live runs (state dot, title
	// over "state · stage", stage-type chip, elapsed over cost), its last
	// finished runs with their MR, and its tickets.
	import { to } from '$lib/guild';
	import { ago, fmtAbs, fmtCost, fmtDuration, plural, stateHue, stateLabel } from '$lib/format';
	import type { Project, Run } from '$lib/domain';
	import Vial from '$lib/ui/Vial.svelte';
	import StageChip from '$lib/ui/StageChip.svelte';
	import { runFacts, stageType } from './runfacts.svelte';

	let {
		project,
		live,
		finished = [],
		liveTotal,
		pings = 0,
		tickets,
		now
	}: {
		project: Project;
		/** Live runs to show (after the desk filter). */
		live: Run[];
		/** The last finished runs (completed / failed). */
		finished?: Run[];
		/** All live runs of the workshop, for the head summary. */
		liveTotal: number;
		pings?: number;
		/** Ticket count; undefined while loading. */
		tickets?: number;
		now: number;
	} = $props();

	const live_ = (s: Run['state']) => s === 'running' || s === 'provisioning';
	const stageLine = (r: Run) =>
		r.state === 'provisioning'
			? 'workspace starting'
			: r.state === 'pending'
				? 'queued'
				: (r.currentStage ?? '—');
</script>

<article class="wc c">
	<header>
		<Vial projectId={project.id} idle={liveTotal === 0} />
		<a class="name ell" href={to.workshop(project.id)}>{project.name}</a>
		<span class="sum">
			{liveTotal ? `${liveTotal} live` : 'idle'}{#if pings}{' · '}<span class="amb"
					>{plural(pings, 'ping')}</span
				>{/if}
		</span>
	</header>

	{#each live as r (r.id)}
		{@const f = runFacts(r)}
		{@const st = stageType(r)}
		<a class="run" href={to.run(r.id)}>
			<span
				class="dot"
				class:pulse={live_(r.state) || r.state === 'waiting_on_human'}
				style:--h={stateHue[r.state]}
			></span>
			<span class="mid">
				<span class="t ell">{r.title || 'Untitled run'}</span>
				<span class="s ell"
					><span style:color={stateHue[r.state]}>{stateLabel[r.state]}</span> ·
					<span class="mono">{stageLine(r)}</span
					>{#if f?.attempt}{` · attempt ${f.attempt}`}{/if}</span
				>
			</span>
			<span class="chip-slot"
				>{#if st}<StageChip type={st} />{/if}</span
			>
			<span class="nums">
				<span>{fmtDuration(now - new Date(r.createdAt).getTime())}</span>
				<span class="fnt">{f ? fmtCost(f.cost) : '…'}</span>
			</span>
		</a>
	{/each}

	{#if finished.length}
		<div class="sec">Last finished</div>
		{#each finished as r (r.id)}
			<div class="run fin">
				<span class="glyph" style:color={stateHue[r.state]}
					>{r.state === 'completed' ? '✓' : r.state === 'failed' ? '✕' : '–'}</span
				>
				<span class="mid">
					<a class="t ell stretch" href={to.run(r.id)}>{r.title || 'Untitled run'}</a>
					<span class="s ell"
						><span style:color={stateHue[r.state]}>{stateLabel[r.state]}</span> ·
						<span title={fmtAbs(r.updatedAt)}>{ago(r.updatedAt)}</span></span
					>
				</span>
				<span></span>
				<span class="mr">
					{#if r.mrUrl}<a href={r.mrUrl} target="_blank" rel="noreferrer">MR ↗</a>{:else}<span
							class="fnt">no MR</span
						>{/if}
				</span>
			</div>
		{/each}
	{/if}

	<footer>
		<a href={to.tickets({ project: project.id })}
			>{tickets === undefined ? 'Tickets' : plural(tickets, 'ticket')}</a
		>
		<span class="spacer"></span>
		<a class="btn btn-sm" href={to.newTicket({ project: project.id })}>+ New ticket</a>
	</footer>
</article>

<style>
	.wc {
		border-radius: 16px;
		display: flex;
		flex-direction: column;
		overflow: hidden;
		min-width: 0;
	}
	header {
		display: flex;
		align-items: center;
		gap: 9px;
		padding: 11px 14px;
	}
	.name {
		font-weight: 600;
		flex: 1;
		color: var(--tx);
	}
	.sum {
		font-size: 11px;
		color: var(--mut);
		white-space: nowrap;
	}
	.amb {
		color: var(--amb);
	}
	.run {
		position: relative;
		display: grid;
		grid-template-columns: 8px minmax(0, 1fr) auto auto;
		gap: 10px;
		align-items: center;
		padding: 8px 14px;
		border-top: 1px solid var(--cLn);
		color: var(--tx);
	}
	.run:hover {
		background: var(--p1);
		text-decoration: none;
	}
	.dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--h);
		box-shadow: 0 0 8px var(--h);
	}
	.glyph {
		font-size: 11px;
		font-weight: 700;
		text-align: center;
	}
	.mid {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.t {
		font-size: 12.5px;
		font-weight: 500;
		color: var(--tx);
	}
	a.t:hover {
		text-decoration: none;
	}
	.stretch::after {
		content: '';
		position: absolute;
		inset: 0;
	}
	.s {
		font-size: 11px;
		color: var(--mut);
	}
	.s .mono {
		font-size: 10.5px;
	}
	.nums {
		font-family: var(--mono);
		font-size: 10.5px;
		color: var(--mut);
		text-align: right;
		white-space: nowrap;
		display: flex;
		flex-direction: column;
		line-height: 1.3;
	}
	.sec {
		padding: 9px 14px 3px;
		border-top: 1px solid var(--cLn);
		font-size: 11px;
		font-weight: 500;
		color: var(--fnt);
	}
	.sec + .run {
		border-top: 0;
	}
	.fin .t {
		color: var(--mut);
	}
	.mr {
		position: relative;
		z-index: 1;
		font-size: 11.5px;
		white-space: nowrap;
	}
	.mr a {
		color: var(--brand);
		font-weight: 600;
	}
	footer {
		margin-top: auto;
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 10px 8px 14px;
		border-top: 1px solid var(--cLn);
		font-size: 12px;
	}
	footer > a:first-child {
		color: var(--mut);
	}
	.spacer {
		flex: 1;
	}
</style>
