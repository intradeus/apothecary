<script lang="ts">
	// Workshop merge requests tab: the repository's open merge requests —
	// what the repository integration brings in — and the runs done on each.
	// One stacked on another (it targets that one's branch) says so, here and
	// when a run is started on it: what it merges lands in that branch. Any
	// flow of the workshop whose runs are started on a merge request
	// (operates_on: merge_request) starts on one from here; which flow, and so
	// what the run does, is the person's pick. A merge request belongs to no
	// ticket: its runs are the ones with it in their data, and its page
	// (title link) is where a run is started with every option.
	import { errorMessage } from '$lib/core/errors';
	import { goto } from '$app/navigation';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type { Flow, MergeRequestRow, Project } from '$lib/domain';
	import { ago, fmtAbs } from '$lib/core/format';
	import { to } from '$lib/core/urls';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import TransportIcon from '$lib/ui/TransportIcon.svelte';
	import { I } from '$lib/design/icons';
	import MrMeta from '$lib/mrs/MrMeta.svelte';

	let { project, flows }: { project: Project; flows: Flow[] | null } = $props();

	let rows = $state<MergeRequestRow[] | null>(null);
	let error = $state('');
	let busy = $state<number | null>(null);

	async function load() {
		try {
			rows = await api.projects.mergeRequests(project.id);
			error = '';
		} catch (e) {
			error = errorMessage(e);
		}
	}
	$effect(() => {
		void project.id;
		void load();
		const timer = setInterval(() => !document.hidden && load(), 30_000);
		return () => clearInterval(timer);
	});

	// What can start on a merge request: every flow of the workshop whose
	// runs are started on one.
	const starts = $derived((flows ?? []).filter((f) => f.operatesOn === 'merge_request'));

	// The flow picker, anchored on the row's button.
	let pickFor = $state<MergeRequestRow | null>(null);
	let pickOpen = $state(false);
	let anchor = $state<HTMLElement | null>(null);
	function pick(row: MergeRequestRow, ev: MouseEvent) {
		pickFor = row;
		anchor = ev.currentTarget as HTMLElement;
		pickOpen = true;
	}

	async function start(row: MergeRequestRow, flow: string) {
		pickOpen = false;
		if (busy !== null) return;
		busy = row.mr.iid;
		try {
			const started = await api.projects.startRunOnMR(project.id, row.mr.iid, { flow });
			toast(t('workshop.mrs.started', { flow, iid: String(row.mr.iid) }), {
				action: { label: t('workshop.mrs.openRun'), run: () => void goto(to.run(started.runId)) }
			});
			await load();
		} catch (e) {
			toast(errorMessage(e));
		} finally {
			busy = null;
		}
	}
</script>

<p class="lede">{t('workshop.mrs.lede')}</p>

{#if flows && !starts.length}
	<div class="help hue-warning">{t('workshop.mrs.noFlow')}</div>
{/if}

{#if error && !rows}
	<ErrorCard cause={t('workshop.mrs.loadError')} detail={error} onretry={load} />
{:else if !rows}
	<Skeleton pane lines={4} />
{:else if !rows.length}
	<EmptyState compact title={t('workshop.mrs.emptyTitle')} text={t('workshop.mrs.emptyText')} />
{:else}
	<section class="pane">
		<div class="rows">
			<div class="r head fr">
				<span>{t('workshop.mrs.colMR')}</span><span>{t('workshop.mrs.colBranch')}</span><span
					>{t('workshop.mrs.colRuns')}</span
				><span></span>
			</div>
			{#each rows as row (row.mr.iid)}
				<div class="r fr">
					<span class="mr">
						<span class="title">
							<a class="linklike truncate" href={to.mergeRequest(project.id, row.mr.iid)}
								><span class="mono faint">!{row.mr.iid}</span> {row.mr.title}</a
							>
							<a
								class="faint host"
								href={row.mr.url}
								target="_blank"
								rel="noreferrer"
								title={t('workshop.mrs.onHost')}
								aria-label={t('workshop.mrs.onHost')}>{I.external}</a
							>
						</span>
						<MrMeta {row} />
					</span>
					<span class="mono small truncate" title="{row.mr.source} → {row.mr.target}"
						>{row.mr.source} → {row.mr.target}</span
					>
					<span class="small runs">
						{#if row.run}
							<a href={to.run(row.run.id)} class="linklike" title={fmtAbs(row.run.createdAt)}
								><StatusChip state={row.run.state} size="sm" /></a
							>
							<span class="faint truncate"
								>{row.run.flow} · {ago(row.run.createdAt)}{#if row.runs > 1}
									· <a class="linklike" href={to.mergeRequest(project.id, row.mr.iid)}
										>{t('workshop.mrs.allRuns', { count: row.runs })}</a
									>{/if}</span
							>
						{:else}
							<span class="faint">{t('workshop.mrs.noRun')}</span>
						{/if}
					</span>
					<span class="acts">
						<button
							class="btn-sm"
							disabled={!starts.length || busy !== null}
							aria-busy={busy === row.mr.iid}
							title={starts.length ? '' : t('workshop.mrs.noFlow')}
							onclick={(ev) => pick(row, ev)}
							><TransportIcon kind="play" /> {t('workshop.mrs.start')}</button
						>
					</span>
				</div>
			{/each}
		</div>
	</section>
{/if}

<Popover {anchor} bind:open={pickOpen} placement="below-end" portal>
	<div class="pickbox">
		{#if pickFor?.stackedOn}
			<span class="help hue-warning small"
				>{t('workshop.mrs.stackedPick', {
					iid: String(pickFor.mr.iid),
					base: String(pickFor.stackedOn.iid),
					branch: pickFor.stackedOn.branch
				})}</span
			>
		{/if}
		<span class="muted faint small"
			>{t('workshop.mrs.pick', { iid: String(pickFor?.mr.iid ?? '') })}</span
		>
		{#each starts as f (f.name)}
			<button class="menu-item mono" onclick={() => pickFor && start(pickFor, f.name)}
				>{f.name}</button
			>
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
	.title {
		display: flex;
		align-items: baseline;
		gap: 6px;
		min-width: 0;
	}
	.host {
		flex: none;
		text-decoration: none;
	}
	.truncate {
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
</style>
