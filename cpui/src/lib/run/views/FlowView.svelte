<script lang="ts">
	// The recipe and where the run is in it (DESIGN §6 Flow, 3.4): a stage
	// strip when the window is wide and short, a vertical recipe list when it
	// is tall and narrow. Clicking a stage opens its log.
	import { t } from '$lib/i18n/index.svelte';
	import StageFlask from '$lib/ui/StageFlask.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import { stageRows } from '../derive';
	import type { RunStore } from '../store.svelte';

	let { rs, w, h, onstage }: { rs: RunStore; w: number; h: number; onstage: (id: string) => void } =
		$props();

	const rows = $derived(stageRows(rs.stageDefs ?? [], rs.events, rs.run, rs.now));
	const horizontal = $derived(w > h * 2.2);
	const hash = $derived(rs.run?.flowHash ? `@${rs.run.flowHash.slice(0, 6)}` : '');
	const finished = $derived(rs.run?.state === 'completed');
	const tip = (r: (typeof rows)[number]) =>
		t('runview.flow.tip', {
			parts: [
				r.id,
				r.type ? (r.type === 'human_gate' ? t('runview.flow.tip_gate') : r.type) : '',
				r.agent ?? '',
				r.attempt ? t('runview.flow.tip_attempt', { n: r.attempt }) : ''
			]
				.filter(Boolean)
				.join(' · ')
		});
</script>

{#if rows.length === 0}
	{#if rs.stageDefs === null && rs.run}<Loading text={t('runview.flow.reading')} />{:else}<div
			class="empty-line"
		>
			{t('runview.flow.no_stages')}
		</div>{/if}
{:else if horizontal}
	<div class="strip">
		{#each rows as r (r.id)}
			<div class="cell">
				<button class="rowbtn stage spill {r.status}" title={tip(r)} onclick={() => onstage(r.id)}>
					<StageFlask status={r.status} />
					<span class="txt">
						<span class="id mono ell">{r.id}</span>
						<span class="st ell">{r.line || ' '}</span>
					</span>
				</button>
				<span class="conn"></span>
			</div>
		{/each}
		<span class="fin serif" class:ok={finished}>{t('runview.flow.done')}</span>
	</div>
{:else}
	<div class="view list">
		<div class="title serif">{t('runview.flow.formula')}</div>
		<div class="sub mono fnt">{rs.run?.flowName ?? ''} {hash}</div>
		{#each rows as r, i (r.id)}
			<div class="vrow">
				<div class="rail">
					<StageFlask status={r.status} />
					<span class="vconn" class:last={i === rows.length - 1}></span>
				</div>
				<button class="rowbtn stage card {r.status}" title={tip(r)} onclick={() => onstage(r.id)}>
					<span class="id mono ell">{r.id}</span>
					<span class="st ell">{r.line || ' '}</span>
				</button>
			</div>
		{/each}
		<div class="vfin serif" class:ok={finished}>{t('runview.flow.done')}</div>
	</div>
{/if}

<style>
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
	/* A stage's status change cross-fades its tint, and the flask's liquid
	 * rides the spring to its new level. */
	.stage {
		flex: 1;
		min-width: 0;
		padding: 7px 10px;
		border-radius: var(--r-card);
		background: var(--p1);
		border: 1px solid var(--cLn);
		transition:
			background-color var(--dur-fade) var(--ease-out),
			border-color var(--dur-fade) var(--ease-out),
			box-shadow var(--dur-fade) var(--ease-out),
			opacity var(--dur-fade) var(--ease-out);
	}
	.strip :global(.liquid),
	.list :global(.liquid) {
		transition: height var(--dur-spring) var(--spring);
	}
	.stage.stage:hover {
		background: var(--p2);
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
		color: var(--tx);
	}
	.st {
		font-size: var(--fs-meta);
		color: var(--mut);
	}
	.stage.running {
		background: var(--azBg);
		border-color: var(--azLn);
		box-shadow: 0 0 16px var(--azGlow);
	}
	.running .st {
		color: var(--az);
		font-family: var(--mono);
		font-size: var(--fs-id);
	}
	.stage.waiting {
		background: var(--ambBg);
		border-color: var(--ambLn);
		box-shadow: 0 0 16px var(--ambGlow);
	}
	.waiting .st {
		color: var(--amb);
		font-weight: 600;
	}
	.stage.failed {
		background: var(--rosBg);
		border-color: var(--rosLn);
	}
	.failed .st {
		color: var(--ros);
	}
	.stage.pending {
		background: transparent;
		border: 1px dashed var(--ln2);
		opacity: var(--o-dim);
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
		border-radius: var(--r-card);
		border: 1px dashed var(--ln2);
		font-size: var(--fs-title);
		color: var(--mut);
	}
	.fin.ok,
	.vfin.ok {
		border-color: var(--grnLn);
		color: var(--grn);
	}
	.list {
		padding: 12px;
	}
	.title {
		font-size: var(--fs-section);
		padding: 0 4px 2px;
	}
	.sub {
		font-size: var(--fs-meta);
		padding: 0 4px 14px;
	}
	.vrow {
		flex: none;
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
		border-radius: var(--r-btn);
		line-height: 1.35;
	}
	.vfin {
		flex: none;
		margin-left: 28px;
		padding: 5px 12px;
		border-radius: var(--r-btn);
		border: 1px dashed var(--ln2);
		font-size: var(--fs-title);
		color: var(--mut);
		text-align: center;
	}
</style>
