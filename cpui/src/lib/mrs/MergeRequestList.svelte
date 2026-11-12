<script lang="ts">
	// Every workshop's open merge requests — the other thing runs start on,
	// beside tickets — read from each repository (DESIGN 7.6). A row opens
	// the merge request's page, where a run is started on it. A workshop
	// whose repository will not answer says so above the rest, which still
	// show. Read again every thirty seconds while the tab is visible.
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { ago, fmtAbs } from '$lib/core/format';
	import { to } from '$lib/core/urls';
	import type { MergeRequestRow } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import StatusChip from '$lib/ui/StatusChip.svelte';
	import MrMeta from './MrMeta.svelte';

	let {
		projectId = '',
		query = '',
		count = $bindable(0),
		onclear
	}: {
		/** Only this workshop's, when set. */
		projectId?: string;
		/** Matched against title, author and branches. */
		query?: string;
		/** How many are open across what is read (for the header). */
		count?: number;
		/** Clears the search and the filter, from the "no match" state. */
		onclear?: () => void;
	} = $props();

	type Item = { projectId: string; row: MergeRequestRow };
	let items = $state<Item[] | null>(null);
	let failures = $state<{ projectId: string; error: string }[]>([]);

	const projectIds = $derived(fleet.projects.map((p) => p.id));

	async function load(ids: string[]) {
		const read = await Promise.allSettled(ids.map((id) => api.projects.mergeRequests(id)));
		const got: Item[] = [];
		const failed: typeof failures = [];
		read.forEach((r, i) => {
			if (r.status === 'fulfilled') got.push(...r.value.map((row) => ({ projectId: ids[i], row })));
			else failed.push({ projectId: ids[i], error: errorMessage(r.reason) });
		});
		items = got;
		failures = failed;
	}

	$effect(() => {
		const ids = projectIds;
		if (!fleet.updatedAt) return;
		void load(ids);
		const timer = setInterval(() => !document.hidden && load(ids), 30_000);
		return () => clearInterval(timer);
	});

	const inScope = $derived((items ?? []).filter((i) => !projectId || i.projectId === projectId));
	$effect(() => {
		count = inScope.length;
	});
	const shown = $derived.by(() => {
		const s = query.trim().toLowerCase();
		return inScope.filter(
			({ row: { mr } }) =>
				!s ||
				[mr.title, mr.author, mr.source, mr.target, `!${mr.iid}`].some((v) =>
					v.toLowerCase().includes(s)
				)
		);
	});
	const shownFailures = $derived(failures.filter((f) => !projectId || f.projectId === projectId));
</script>

{#each shownFailures as f (f.projectId)}
	<ErrorCard
		cause={t('page.mr.loadError', { workshop: fleet.projectName(f.projectId) })}
		detail={f.error}
		onretry={() => load(projectIds)}
	/>
{/each}

{#if items === null}
	<Skeleton pane lines={6} />
{:else if inScope.length === 0}
	{#if !shownFailures.length}
		<EmptyState title={t('page.mr.empty')} text={t('page.mr.emptyText')} />
	{/if}
{:else if shown.length === 0}
	<EmptyState compact title={t('page.mr.noMatch')} text={t('page.mr.noMatchText')}>
		{#if onclear}<button onclick={onclear}>{t('page.tickets.clearFilters')}</button>{/if}
	</EmptyState>
{:else}
	<section class="pane">
		<div class="rows">
			<div class="r head" class:all={!projectId}>
				<span>{t('workshop.mrs.colMR')}</span>
				{#if !projectId}<span>{t('page.mr.colWorkshop')}</span>{/if}
				<span>{t('workshop.mrs.colBranch')}</span>
				<span>{t('workshop.mrs.colRuns')}</span>
			</div>
			{#each shown as { projectId: pid, row } (`${pid}/${row.mr.iid}`)}
				<div class="r" class:all={!projectId}>
					<span class="mr">
						<a class="linklike truncate" href={to.mergeRequest(pid, row.mr.iid)}
							><span class="mono faint">!{row.mr.iid}</span> {row.mr.title}</a
						>
						<MrMeta {row} />
					</span>
					{#if !projectId}
						<a class="linklike small truncate" href={to.workshop(pid, 'mrs')}
							>{fleet.projectName(pid)}</a
						>
					{/if}
					<span class="mono small truncate" title="{row.mr.source} → {row.mr.target}"
						>{row.mr.source} → {row.mr.target}</span
					>
					<span class="small last">
						{#if row.run}
							<a href={to.run(row.run.id)} class="linklike" title={fmtAbs(row.run.createdAt)}
								><StatusChip state={row.run.state} size="sm" /></a
							>
							<span class="faint truncate">{row.run.flow} · {ago(row.run.createdAt)}</span>
						{:else}
							<span class="faint">{t('workshop.mrs.noRun')}</span>
						{/if}
					</span>
				</div>
			{/each}
		</div>
	</section>
{/if}

<style>
	.r {
		grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr) minmax(0, 1.2fr);
	}
	.r.all {
		grid-template-columns: minmax(0, 1.6fr) minmax(0, 0.7fr) minmax(0, 1fr) minmax(0, 1.2fr);
	}
	.mr {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}
	.truncate {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.last {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
</style>
