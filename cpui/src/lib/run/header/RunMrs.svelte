<script lang="ts">
	// The run's merge requests, as the top bar's island.
	import { api } from '$lib/api';
	import { fleet } from '$lib/app/fleet.svelte';
	import { onMount } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';
	import type { MergeRequestLive } from '$lib/domain';
	import { I } from '$lib/design/icons';
	import MrIsland, { type MrRow } from '../MrIsland.svelte';
	import type { RunStore } from '../store.svelte';

	let { rs }: { rs: RunStore } = $props();

	const run = $derived(rs.run!);
	const project = $derived(run.projectId ? fleet.projectById.get(run.projectId) : undefined);
	const active = $derived(!rs.terminal);

	// What the repository host says about the run's merge requests right now, polled
	// while the run is alive; an observation, not part of the run's record.
	let mrLive = $state<MergeRequestLive[]>([]);
	const hasMr = $derived(run.mrs.length > 0 || !!run.mrUrl);
	async function loadMrs() {
		if (!hasMr) return;
		try {
			mrLive = await api.runs.mrStatus(run.id);
		} catch {
			// The header must not break on a repository host hiccup; the next poll retries.
		}
	}
	onMount(() => {
		void loadMrs();
		const timer = setInterval(() => active && !document.hidden && loadMrs(), 30_000);
		return () => clearInterval(timer);
	});

	// The run's merge requests: the one it was created on, the ones it opened
	// and — only when its flow opens merge requests — a "not yet" slot for each
	// checkout still without one. A run that has none and whose flow opens none
	// shows no island at all. Live status joins by URL; merged ones sink to the
	// end, so the pill leads with what is still in play.
	const mrRows = $derived.by(() => {
		const rows: MrRow[] = [];
		const seen = new Set<string>();
		const live = (url: string) => mrLive.find((m) => m.url === url);
		const a = run.adopted;
		if (a) {
			seen.add(a.repo);
			rows.push({
				key: a.url,
				label: `!${a.iid}`,
				url: a.url,
				title: t('run.mr.adopted', { title: a.title || `!${a.iid}`, author: a.author ?? '?' }),
				live: live(a.url)
			});
		}
		for (const mr of run.mrs) {
			seen.add(mr.repo);
			if (mr.url === a?.url) continue;
			rows.push({
				key: mr.url,
				label: `!${mr.iid}`,
				url: mr.url,
				title: `${mr.repo !== 'repo' ? mr.repo + ': ' : ''}${mr.branch}${mr.target ? ' → ' + mr.target : ''}`,
				live: live(mr.url)
			});
		}
		for (const c of rs.opensMRs ? run.checkouts : []) {
			const key = c.primary ? 'repo' : c.name;
			if (seen.has(key) || seen.has(c.name)) continue;
			rows.push({ key: c.name, label: I.none, title: t('run.mr.notOpenedRepo', { name: c.name }) });
		}
		// Runs from before multi-repo carry only mrUrl, no mrs list.
		if (!rows.length && run.mrUrl)
			rows.push({
				key: run.mrUrl,
				label: t('run.mr.one'),
				url: run.mrUrl,
				title: run.branch ?? t('run.mr.title'),
				live: live(run.mrUrl)
			});
		if (!rows.length && rs.opensMRs)
			rows.push({ key: 'none', label: I.none, title: t('run.mr.notOpened') });
		return [...rows].sort(
			(x, y) =>
				Number(!x.url) - Number(!y.url) ||
				Number(x.live?.state === 'merged') - Number(y.live?.state === 'merged')
		);
	});
	const mrTarget = $derived.by(() => {
		const m = run.mrs[0];
		if (!m) return undefined;
		return `${project?.name ?? m.repo} → ${m.target ?? run.sourceBranch ?? t('run.header.default')}`;
	});
</script>

{#if mrRows.length}<MrIsland rows={mrRows} target={mrTarget} multiRepo={mrLive.length > 1} />{/if}
