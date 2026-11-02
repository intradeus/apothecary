<script lang="ts">
	// The recipe and where the run is in it (DESIGN §6 Flow, 3.4): a stage
	// strip when the window is wide and short, a vertical recipe list when it
	// is tall and narrow, grouped by phase once more than one has run.
	// Clicking a stage opens its log.
	import { t } from '$lib/i18n/index.svelte';
	import StageFlask from '$lib/ui/StageFlask.svelte';
	import { I } from '$lib/design/icons';
	import { STAGE_WORD } from '$lib/design/hues';
	import Loading from '$lib/ui/Loading.svelte';
	import { stateLabel } from '$lib/core/format';
	import { shownStages, stageRows } from '../derive';
	import type { RunStore } from '../store.svelte';

	let { rs, w, h, onstage }: { rs: RunStore; w: number; h: number; onstage: (id: string) => void } =
		$props();

	const rows = $derived(
		stageRows(shownStages(rs.stageDefs ?? [], rs.events), rs.events, rs.run, rs.now)
	);
	const grouped = $derived(new Set(rows.map((r) => r.phase ?? '')).size > 1);
	/** The phase a row opens, when the view is grouped and it is the first of it. */
	const opens = (i: number) =>
		grouped && (i === 0 || rows[i - 1].phase !== rows[i].phase) ? (rows[i].phase ?? '') : '';
	const horizontal = $derived(w > h * 2.2);
	/** The tile's top line: how it went, and what kind of stage it is. */
	const kind = (r: (typeof rows)[number]) =>
		[
			r.status === 'done'
				? I.ok
				: r.status === 'failed'
					? I.bad
					: r.status === 'running'
						? I.dot
						: '',
			r.type ? (STAGE_WORD[r.type] ?? r.type) : '',
			r.status === 'running' && r.attempt ? t('runview.flow.tip_attempt', { n: r.attempt }) : ''
		]
			.filter(Boolean)
			.join(' ')
			.replace(/ (attempt)/, ' · $1');
	const hash = $derived(rs.run?.flowHash ? `@${rs.run.flowHash.slice(0, 6)}` : '');
	const finished = $derived(rs.run?.state === 'completed');
	const tip = (r: (typeof rows)[number]) =>
		t('runview.flow.tip', {
			parts: [
				r.id,
				r.type
					? r.type === 'human_gate'
						? t('runview.flow.tip_gate')
						: r.type === 'manual'
							? t('runview.flow.tip_manual')
							: r.type
					: '',
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
		{#each rows as r, i (r.id)}
			{#if opens(i)}<span class="ph mono faint" title={t('runview.flow.phase')}>{opens(i)}</span
				>{/if}
			<div class="cell">
				<button
					class="rowbtn stage spill {r.status}"
					class:gate={r.type === 'human_gate'}
					title={tip(r)}
					onclick={() => onstage(r.id)}
				>
					<StageFlask status={r.status} />
					<span class="txt">
						<span class="kd truncate">{kind(r)}</span>
						<span class="id mono truncate">{r.id}</span>
						<span class="st truncate tick"
							>{r.status === 'pending' ? stateLabel.pending.toLowerCase() : r.line || ' '}</span
						>
					</span>
				</button>
			</div>
		{/each}
		<span class="fin display" class:ok={finished}>{t('runview.flow.done')}</span>
	</div>
{:else}
	<div class="view list">
		<div class="title display">{t('runview.flow.formula')}</div>
		<div class="sub mono faint">{rs.run?.flowName ?? ''} {hash}</div>
		{#each rows as r, i (r.id)}
			{#if opens(i)}<div class="vph mono faint" title={t('runview.flow.phase')}>
					{opens(i)}
				</div>{/if}
			<div class="vrow">
				<div class="rail">
					<StageFlask status={r.status} />
					<span class="vconn" class:last={i === rows.length - 1}></span>
				</div>
				<button class="rowbtn stage card {r.status}" title={tip(r)} onclick={() => onstage(r.id)}>
					<span class="id mono truncate">{r.id}</span>
					<span class="st truncate tick">{r.line || ' '}</span>
				</button>
			</div>
		{/each}
		<div class="vfin display" class:ok={finished}>{t('runview.flow.done')}</div>
	</div>
{/if}

<style>
	.strip {
		flex: 1;
		min-height: 0;
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 8px 12px 12px;
		overflow-x: auto;
		overflow-y: hidden;
	}
	.ph {
		flex: none;
		font-size: var(--fs-meta);
		padding: 0 8px 0 4px;
		white-space: nowrap;
	}
	.vph {
		font-size: var(--fs-meta);
		padding: 8px 0 4px;
	}
	.cell {
		display: flex;
		align-items: center;
		flex: 1;
		min-width: 108px;
	}
	/* A stage's status change cross-fades its tint, and the flask's liquid
	 * rides the spring to its new level. */
	.stage {
		--tone: var(--text-muted);
		flex: 1;
		min-width: 0;
		height: 100%;
		padding: 8px 11px;
		border-radius: var(--r-row);
		background: var(--fill-1);
		border: 1px solid transparent;
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
		background: var(--fill-2);
		border-color: var(--line-strong);
	}
	.txt {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 1px;
		line-height: 1.3;
	}
	.kd {
		font-size: var(--fs-meta);
		color: var(--tone);
	}
	.id {
		color: var(--text);
		font-size: var(--fs-ui);
	}
	.st {
		font-size: var(--fs-meta);
		color: var(--text-faint);
	}
	/* A window too short for three lines keeps the name and the status. */
	@container (max-height: 74px) {
		.kd {
			display: none;
		}
	}
	.stage.done {
		--tone: var(--success);
		background: color-mix(in oklch, var(--tone) 8%, transparent);
	}
	.stage.done.gate {
		--tone: var(--warning);
	}
	.stage.running {
		--tone: var(--running-text);
		background: var(--running-fill);
		border-color: var(--running-line);
		box-shadow: 0 0 24px var(--running-glow);
	}
	.running .id {
		font-weight: 600;
	}
	.running .st {
		color: var(--running-text);
	}
	.stage.waiting {
		--tone: var(--attention-text);
		background: var(--attention-bg);
		border-color: var(--attention-line);
		box-shadow: 0 0 24px var(--attention-glow);
	}
	.waiting .st {
		color: var(--attention-text);
		font-weight: 600;
	}
	.stage.failed {
		--tone: var(--danger-text);
		background: var(--danger-bg);
		border-color: var(--danger-line);
	}
	.failed .st {
		color: var(--danger-text);
	}
	.stage.pending {
		opacity: 0.65;
	}
	.pending .st {
		color: var(--text-faint);
	}
	.fin {
		flex: none;
		display: flex;
		align-items: center;
		align-self: stretch;
		padding: 0 14px;
		border-radius: var(--r-row);
		border: 1px dashed var(--line-strong);
		font-size: var(--fs-lead);
		color: var(--text-muted);
		opacity: 0.6;
	}
	.fin.ok,
	.vfin.ok {
		opacity: 1;
		border-color: var(--success-line);
		color: var(--success-text);
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
		background: var(--line-strong);
	}
	.card {
		flex-direction: column;
		align-items: stretch;
		gap: 0;
		height: auto;
		padding: 6px 10px;
		margin-bottom: 8px;
		border-radius: var(--r-btn);
		line-height: 1.35;
	}
	.vfin {
		flex: none;
		margin-left: 28px;
		padding: 5px 12px;
		border-radius: var(--r-btn);
		border: 1px dashed var(--line-strong);
		font-size: var(--fs-title);
		color: var(--text-muted);
		text-align: center;
	}
</style>
