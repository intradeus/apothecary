<script lang="ts">
	// Workshop reviews tab: the repository's open merge requests and the
	// review the factory did of each — start one, follow it, run it again. A
	// review is a run of a flow with an entrypoint on mr_opened (the built-in
	// reviewer by default), created on the merge request and filed under the
	// merge request's own ticket: the same one a forge_mr automation would
	// use, so the two never review one twice.
	import { goto } from '$app/navigation';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type { Flow, Project, ReviewRow } from '$lib/domain';
	import { MR_OPENED } from '$lib/flowdoc';
	import { ago, fmtAbs, isTerminal } from '$lib/format';
	import { to } from '$lib/guild';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';

	let { project, flows }: { project: Project; flows: Flow[] | null } = $props();

	let rows = $state<ReviewRow[] | null>(null);
	let error = $state('');
	let busy = $state<number | null>(null);
	const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));

	async function load() {
		try {
			rows = await api.projects.reviews(project.id);
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

	// The flows that can review: an entrypoint on mr_opened. The choice is
	// remembered per workshop, in this browser.
	const reviewFlows = $derived(
		(flows ?? []).filter((f) => f.phases.some((p) => p.signal === MR_OPENED))
	);
	const storeKey = $derived(`apothecary.reviewFlow.${project.id}`);
	let flowPick = $state('');
	$effect(() => {
		const names = reviewFlows.map((f) => f.name);
		if (names.includes(flowPick)) return;
		let saved = '';
		try {
			saved = localStorage.getItem(storeKey) ?? '';
		} catch {
			/* no storage: the default below */
		}
		flowPick = names.includes(saved)
			? saved
			: names.includes('reviewer')
				? 'reviewer'
				: (names[0] ?? '');
	});
	function pickFlow(name: string) {
		flowPick = name;
		try {
			localStorage.setItem(storeKey, name);
		} catch {
			/* a per-browser convenience only */
		}
	}

	async function review(row: ReviewRow) {
		if (busy !== null || !flowPick) return;
		busy = row.mr.iid;
		try {
			const started = await api.projects.startReview(project.id, row.mr.iid, { flow: flowPick });
			toast(t('workshop.reviews.started', { iid: row.mr.iid }), {
				action: {
					label: t('workshop.reviews.openRun'),
					run: () => void goto(to.run(started.runId))
				}
			});
			await load();
		} catch (e) {
			toast(msg(e));
		} finally {
			busy = null;
		}
	}

	const open = $derived((rows ?? []).filter((r) => !r.mine));
	const factory = $derived((rows ?? []).filter((r) => r.mine));
</script>

<div class="row nowrap">
	<p class="lede grow">{t('workshop.reviews.lede')}</p>
	{#if reviewFlows.length}
		<label class="flowpick small">
			<span class="fnt">{t('workshop.reviews.flow')}</span>
			<select value={flowPick} onchange={(e) => pickFlow(e.currentTarget.value)}>
				{#each reviewFlows as f (f.name)}<option value={f.name}>{f.name}</option>{/each}
			</select>
		</label>
	{/if}
</div>

{#if flows && !reviewFlows.length}
	<div class="help hue-amb">{t('workshop.reviews.noFlow')}</div>
{/if}

{#if error && !rows}
	<ErrorCard cause={t('workshop.reviews.loadError')} detail={error} onretry={load} />
{:else if !rows}
	<Skeleton pane lines={4} />
{:else if !rows.length}
	<section class="c pane">
		<div class="pane-body">
			<EmptyState
				compact
				title={t('workshop.reviews.emptyTitle')}
				text={t('workshop.reviews.emptyText')}
			/>
		</div>
	</section>
{:else}
	<section class="c pane">
		<div class="pane-head">
			<h2>{t('page.workshop.tabReviews')}</h2>
			<span class="count">{open.length}</span>
		</div>
		<div class="rows">
			<div class="r head rr">
				<span>{t('workshop.reviews.colMR')}</span><span>{t('workshop.reviews.colBranch')}</span
				><span>{t('workshop.reviews.colReview')}</span><span></span>
			</div>
			{#each [...open, ...factory] as row (row.mr.iid)}
				<div class="r rr">
					<span class="mr">
						<a class="linklike ell" href={row.mr.url} target="_blank" rel="noreferrer"
							><span class="mono fnt">!{row.mr.iid}</span> {row.mr.title}</a
						>
						<span class="small fnt"
							>{row.mr.author}{#if row.mine}
								· <span class="pill sm t-brand" title={t('workshop.reviews.mineTitle')}
									>{t('workshop.reviews.mine')}</span
								>{/if}{#if row.mr.draft}
								· <span class="pill sm t-mut">{t('workshop.reviews.draft')}</span
								>{/if}{#if row.mr.fork}
								· <span class="pill sm t-mut">{t('workshop.reviews.fork')}</span>{/if}</span
						>
					</span>
					<span class="mono small ell" title="{row.mr.source} → {row.mr.target}"
						>{row.mr.source} → {row.mr.target}</span
					>
					<span class="small state">
						{#if row.run}
							<a href={to.run(row.run.id)} class="linklike" title={fmtAbs(row.run.createdAt)}
								><StatusChip state={row.run.state} size="sm" /></a
							>
							<span class="fnt"
								>{t('workshop.reviews.lastRun', {
									ago: ago(row.run.createdAt),
									count: row.runs
								})}</span
							>
						{:else}
							<span class="fnt">{t('workshop.reviews.notYet')}</span>
						{/if}
					</span>
					<span class="acts">
						{#if row.run && !isTerminal(row.run.state)}
							<a class="btn-sm" href={to.run(row.run.id)}>{t('workshop.reviews.follow')}</a>
						{:else}
							<button
								class={row.run ? 'btn-sm' : 'btn-sm btn-primary'}
								disabled={!flowPick || busy !== null}
								aria-busy={busy === row.mr.iid}
								onclick={() => review(row)}
								>{I.play}
								{row.run ? t('workshop.reviews.again') : t('workshop.reviews.start')}</button
							>
						{/if}
					</span>
				</div>
			{/each}
		</div>
	</section>
{/if}

<style>
	.rr {
		grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr) minmax(0, 1fr) auto;
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
	.state {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}
	.acts {
		display: flex;
		gap: 6px;
		justify-content: flex-end;
	}
	.flowpick {
		display: flex;
		align-items: center;
		gap: 8px;
	}
</style>
