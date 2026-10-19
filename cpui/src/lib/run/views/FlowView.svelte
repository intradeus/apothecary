<script lang="ts">
	// The recipe and where the run is in it (DESIGN §6 Flow, 3.4): a stage
	// strip when the window is wide and short, a vertical recipe list when it
	// is tall and narrow. Clicking a stage opens its log.
	import StageFlask from '$lib/ui/StageFlask.svelte';
	import { lore } from '$lib/lore';
	import { stageRows } from '../derive';
	import type { RunStore } from '../store.svelte';

	let { rs, w, h, onstage }: { rs: RunStore; w: number; h: number; onstage: (id: string) => void } =
		$props();

	const rows = $derived(stageRows(rs.stageDefs ?? [], rs.events, rs.run, rs.now));
	const horizontal = $derived(w > h * 2.2);
	const hash = $derived(rs.run?.flowHash ? `@${rs.run.flowHash.slice(0, 6)}` : '');
	const finished = $derived(rs.run?.state === 'completed');
	const tip = (r: (typeof rows)[number]) =>
		`${r.id}${r.type ? ` · ${r.type === 'human_gate' ? 'gate' : r.type}` : ''}${r.agent ? ` · ${r.agent}` : ''}${r.attempt ? ` · attempt ${r.attempt}` : ''} — open its log`;
</script>

{#if rows.length === 0}
	<div class="none">
		{#if rs.stageDefs === null && rs.run}<span class="dot pulse"></span>Reading the flow…{:else}No
			stages yet.{/if}
	</div>
{:else if horizontal}
	<div class="strip">
		{#each rows as r (r.id)}
			<div class="cell">
				<button class="spill {r.status}" title={tip(r)} onclick={() => onstage(r.id)}>
					<StageFlask status={r.status} />
					<span class="txt">
						<span class="id">{r.id}</span>
						<span class="st">{r.line || ' '}</span>
					</span>
				</button>
				<span class="conn"></span>
			</div>
		{/each}
		<span class="fin" class:ok={finished}>done</span>
	</div>
{:else}
	<div class="list">
		<div class="title">{lore('flow')}</div>
		<div class="sub">{rs.run?.flowName ?? ''} {hash}</div>
		{#each rows as r, i (r.id)}
			<div class="vrow">
				<div class="rail">
					<StageFlask status={r.status} />
					<span class="vconn" class:last={i === rows.length - 1}></span>
				</div>
				<button class="card {r.status}" title={tip(r)} onclick={() => onstage(r.id)}>
					<span class="id">{r.id}</span>
					<span class="st">{r.line || ' '}</span>
				</button>
			</div>
		{/each}
		<div class="vfin" class:ok={finished}>done</div>
	</div>
{/if}

<style>
	.none {
		margin: auto;
		color: var(--mut);
		font-size: 12px;
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--az);
	}
	.strip {
		flex: 1;
		min-height: 0;
		display: flex;
		align-items: center;
		padding: 6px 8px;
		overflow-x: auto;
		overflow-y: hidden;
	}
	.cell {
		display: flex;
		align-items: center;
		flex: 1;
		min-width: 92px;
	}
	.conn {
		flex: none;
		width: 10px;
		height: 1px;
		background: var(--ln2);
	}
	.spill,
	.card {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		flex: 1;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 10px;
		border-radius: 14px;
		background: var(--p1);
		border: 1px solid var(--cLn);
	}
	.spill:hover,
	.card:hover {
		border-color: var(--ln2);
	}
	.txt {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		line-height: 1.3;
	}
	.id {
		font-family: var(--mono);
		font-size: 11.5px;
		font-weight: 500;
		color: var(--tx);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.st {
		font-size: 11px;
		color: var(--mut);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.running {
		background: var(--azBg);
		border-color: var(--azLn);
		box-shadow: 0 0 16px var(--azGlow);
	}
	.running .st {
		color: var(--az);
		font-family: var(--mono);
		font-size: 10.5px;
	}
	.waiting {
		background: var(--ambBg);
		border-color: var(--ambLn);
		box-shadow: 0 0 16px var(--ambGlow);
	}
	.waiting .st {
		color: var(--amb);
		font-weight: 600;
	}
	.failed {
		background: var(--rosBg);
		border-color: var(--rosLn);
	}
	.failed .st {
		color: var(--ros);
	}
	.paused .st {
		color: var(--mut);
	}
	.pending {
		background: transparent;
		border: 1px dashed var(--ln2);
		opacity: 0.55;
	}
	.pending .st {
		color: var(--fnt);
	}
	.fin {
		flex: none;
		display: flex;
		align-items: center;
		align-self: stretch;
		margin: 4px 0;
		padding: 0 14px;
		border-radius: 14px;
		border: 1px dashed var(--ln2);
		font-family: var(--serif);
		font-style: italic;
		font-size: 16px;
		color: var(--mut);
	}
	.fin.ok,
	.vfin.ok {
		border-color: var(--grnLn);
		color: var(--grn);
	}
	.list {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 12px;
		display: flex;
		flex-direction: column;
	}
	.title {
		font-family: var(--serif);
		font-size: 20px;
		padding: 0 4px 2px;
	}
	.sub {
		font-family: var(--mono);
		font-size: 11px;
		color: var(--fnt);
		padding: 0 4px 14px;
	}
	.vrow {
		display: flex;
		gap: 10px;
		align-items: stretch;
	}
	.rail {
		flex: none;
		width: 18px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 4px;
		padding-top: 9px;
	}
	.vconn {
		flex: 1;
		width: 1px;
		min-height: 8px;
		background: var(--ln2);
	}
	.card {
		flex-direction: column;
		align-items: stretch;
		gap: 0;
		padding: 6px 10px;
		margin-bottom: 6px;
		border-radius: 12px;
		line-height: 1.35;
	}
	.vfin {
		margin-left: 28px;
		padding: 5px 12px;
		border-radius: 12px;
		border: 1px dashed var(--ln2);
		font-family: var(--serif);
		font-style: italic;
		font-size: 16px;
		color: var(--mut);
		text-align: center;
	}
</style>
