<script lang="ts">
	// Workshop forge tab: what the forge brings in — the repository's open
	// merge requests — and the runs done on each. Any flow of the workshop
	// that can begin on a merge request (an entrypoint on mr_opened) starts
	// on one from here; which flow, and so what the run does, is the
	// person's pick. A merge request's runs are filed under its own ticket,
	// the same one a forge_mr automation would use.
	import { goto } from '$app/navigation';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type { Flow, MergeRequestRow, Project } from '$lib/domain';
	import { MR_OPENED } from '$lib/flowdoc';
	import { ago, fmtAbs } from '$lib/format';
	import { to } from '$lib/guild';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';

	let { project, flows }: { project: Project; flows: Flow[] | null } = $props();

	let rows = $state<MergeRequestRow[] | null>(null);
	let error = $state('');
	let busy = $state<number | null>(null);
	const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));

	async function load() {
		try {
			rows = await api.projects.mergeRequests(project.id);
			error = '';
		} catch (e) {
			error = msg(e);
		}
	}
	$effect(() => {
		void project.id;
		void load();
		const timer = setInterval(() => !document.hidden && load(), 30_000);
		return () => clearInterval(timer);
	});

	// What can start on a merge request: every entrypoint on mr_opened of
	// every flow of the workshop.
	const starts = $derived(
		(flows ?? []).flatMap((f) =>
			f.phases
				.filter((p) => p.signal === MR_OPENED)
				.map((p, _, all) => ({
					flow: f.name,
					phase: p.id,
					label: all.length > 1 ? `${f.name} · ${p.id}` : f.name,
					description: p.description
				}))
		)
	);

	// The flow picker, anchored on the row's button.
	let pickFor = $state<MergeRequestRow | null>(null);
	let pickOpen = $state(false);
	let anchor = $state<HTMLElement | null>(null);
	function pick(row: MergeRequestRow, ev: MouseEvent) {
		pickFor = row;
		anchor = ev.currentTarget as HTMLElement;
		pickOpen = true;
	}

	async function start(row: MergeRequestRow, flow: string, phase: string) {
		pickOpen = false;
		if (busy !== null) return;
		busy = row.mr.iid;
		try {
			const started = await api.projects.startRunOnMR(project.id, row.mr.iid, { flow, phase });
			toast(t('workshop.forge.started', { flow, iid: row.mr.iid }), {
				action: { label: t('workshop.forge.openRun'), run: () => void goto(to.run(started.runId)) }
			});
			await load();
		} catch (e) {
			toast(msg(e));
		} finally {
			busy = null;
		}
	}
</script>

<div class="row nowrap">
	<p class="lede grow">{t('workshop.forge.lede')}</p>
</div>

{#if flows && !starts.length}
	<div class="help hue-amb">{t('workshop.forge.noFlow')}</div>
{/if}

{#if error && !rows}
	<ErrorCard cause={t('workshop.forge.loadError')} detail={error} onretry={load} />
{:else if !rows}
	<Skeleton pane lines={4} />
{:else if !rows.length}
	<section class="c pane">
		<div class="pane-body">
			<EmptyState
				compact
				title={t('workshop.forge.emptyTitle')}
				text={t('workshop.forge.emptyText')}
			/>
		</div>
	</section>
{:else}
	<section class="c pane">
		<div class="pane-head">
			<h2>{t('workshop.forge.title')}</h2>
			<span class="count">{rows.length}</span>
		</div>
		<div class="rows">
			<div class="r head fr">
				<span>{t('workshop.forge.colMR')}</span><span>{t('workshop.forge.colBranch')}</span><span
					>{t('workshop.forge.colRuns')}</span
				><span></span>
			</div>
			{#each rows as row (row.mr.iid)}
				<div class="r fr">
					<span class="mr">
						<a class="linklike ell" href={row.mr.url} target="_blank" rel="noreferrer"
							><span class="mono fnt">!{row.mr.iid}</span> {row.mr.title}</a
						>
						<span class="small fnt"
							>{row.mr.author}{#if row.mine}
								· <span class="pill sm t-brand" title={t('workshop.forge.mineTitle')}
									>{t('workshop.forge.mine')}</span
								>{/if}{#if row.mr.draft}
								· <span class="pill sm t-mut">{t('workshop.forge.draft')}</span
								>{/if}{#if row.mr.fork}
								· <span class="pill sm t-mut">{t('workshop.forge.fork')}</span>{/if}</span
						>
					</span>
					<span class="mono small ell" title="{row.mr.source} → {row.mr.target}"
						>{row.mr.source} → {row.mr.target}</span
					>
					<span class="small runs">
						{#if row.run}
							<a href={to.run(row.run.id)} class="linklike" title={fmtAbs(row.run.createdAt)}
								><StatusChip state={row.run.state} size="sm" /></a
							>
							<span class="fnt ell"
								>{row.run.flow} · {ago(row.run.createdAt)}{#if row.ticketId && row.runs > 1}
									· <a class="linklike" href={to.ticket(row.ticketId)}
										>{t('workshop.forge.allRuns', { count: row.runs })}</a
									>{/if}</span
							>
						{:else}
							<span class="fnt">{t('workshop.forge.noRun')}</span>
						{/if}
					</span>
					<span class="acts">
						<button
							class="btn-sm"
							disabled={!starts.length || busy !== null}
							aria-busy={busy === row.mr.iid}
							title={starts.length ? '' : t('workshop.forge.noFlow')}
							onclick={(ev) => pick(row, ev)}>{I.play} {t('workshop.forge.start')}</button
						>
					</span>
				</div>
			{/each}
		</div>
	</section>
{/if}

<Popover {anchor} bind:open={pickOpen} placement="below-end">
	<div class="pickbox">
		<span class="mut fnt small">{t('workshop.forge.pick', { iid: pickFor?.mr.iid ?? '' })}</span>
		{#each starts as s (s.flow + '|' + s.phase)}
			<button class="opt" onclick={() => pickFor && start(pickFor, s.flow, s.phase)}>
				<span class="mono">{s.label}</span>
				{#if s.description}<span class="fnt small">{s.description}</span>{/if}
			</button>
		{/each}
	</div>
</Popover>

<style>
	.fr {
		grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr) minmax(0, 1.2fr) auto;
	}
	.mr {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}
	.ell {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.runs {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.acts {
		display: flex;
		justify-content: flex-end;
	}
	.pickbox {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 8px;
		min-width: 220px;
		max-width: 360px;
	}
	.opt {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 1px;
		padding: 6px 8px;
		border-radius: var(--r-sm, 8px);
		text-align: left;
		cursor: pointer;
	}
	.opt:hover {
		background: var(--p3);
	}
</style>
