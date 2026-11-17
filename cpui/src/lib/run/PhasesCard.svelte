<script lang="ts">
	// The flow's chains (FLOWLANG §1), one card each under its kind: the main
	// chain the run started with, the watchers and the outcomes that end the
	// run (PhaseCard). Any watcher can be started by hand while no chain
	// holds the run's one cursor; this reads the run, starts one and asks on
	// which merge request when several are open.
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type { MergeRequestLive, RunPhase } from '$lib/domain';
	import { PHASE_KINDS } from '$lib/domain/flowdoc';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/design/icons';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import MrStatus from './MrStatus.svelte';
	import PhaseCard from './PhaseCard.svelte';

	let {
		runId,
		idle,
		ended = false
	}: {
		runId: string;
		idle: boolean;
		/** The run is over: what ran, without anything to start. */
		ended?: boolean;
	} = $props();
	let mrs = $state<MergeRequestLive[]>([]);
	let phases = $state<RunPhase[]>([]);
	let titles = $state<Record<string, string>>({});
	let busy = $state('');
	let error = $state('');
	let cause = $state('');
	// The subject picker: which phase's Run button opened it, anchored there.
	let pick = $state('');
	let pickOpen = $state(false);
	let anchor = $state<HTMLElement | null>(null);

	const open = $derived(mrs.filter((m) => m.state === 'open'));

	async function load() {
		try {
			[mrs, phases] = await Promise.all([api.runs.mrStatus(runId), api.runs.phases(runId)]);
			// A read that works ends a failed read, not a phase that failed to start.
			if (cause === t('run.phases.repoError')) error = '';
		} catch (e) {
			cause = t('run.phases.repoError');
			error = errorMessage(e);
		}
	}

	async function start(phase: string, subject = '') {
		pickOpen = false;
		busy = phase;
		error = '';
		try {
			await api.runs.startPhase(runId, phase, subject);
			toast(t('run.phases.phaseStarted', { phase }));
		} catch (e) {
			cause = t('run.phases.phaseFailed', { phase });
			error = errorMessage(e);
		} finally {
			busy = '';
			load();
		}
	}

	function run(p: RunPhase, ev: MouseEvent) {
		if (open.length > 1) {
			pick = p.id;
			anchor = ev.currentTarget as HTMLElement;
			pickOpen = true;
			return;
		}
		start(p.id);
	}

	const spent = (p: RunPhase) =>
		p.max !== -1 &&
		(open.length ? open.every((m) => (p.runs[m.repo] ?? 0) >= p.max) : (p.runs[''] ?? 0) >= p.max);
	// One cursor: a chain parked at a wait point still holds it.
	const free = $derived(idle && !phases.some((p) => p.live));

	onMount(() => {
		load();
		api.system
			.signals()
			.then((list) => (titles = Object.fromEntries(list.map((s) => [s.slug, s.title]))))
			.catch(() => {});
		const timer = setInterval(() => !document.hidden && load(), 30_000);
		return () => clearInterval(timer);
	});
</script>

{#if phases.length}
	<div class="pc">
		{#if open.length}
			<div class="mrs card-in">
				<span class="caption">{t('run.phases.mrs')}</span>
				{#each open as mr (mr.repo + mr.branch)}
					<span class="mr">
						<a href={mr.url} target="_blank" rel="noreferrer" class="mono linklike brand"
							>{mr.repo === 'repo' ? mr.branch : `${mr.repo}: ${mr.branch}`} {I.external}</a
						>
						<MrStatus {mr} long />
					</span>
				{/each}
			</div>
		{/if}
		{#if error}{@render failure()}{/if}
		{#each PHASE_KINDS as kind (kind)}
			{@const list = phases.filter((p) => p.kind === kind)}
			{#if list.length}
				<section aria-label={t(`run.phases.section.${kind}`)}>
					<SectionHeader
						title={t(`run.phases.section.${kind}`)}
						count={list.length}
						sub={t(`run.phases.sectionSub.${kind}`)}
					/>
					<div class="card-grid" style:--min="280px">
						{#each list as p (p.id)}
							<PhaseCard
								phase={p}
								{titles}
								{ended}
								{free}
								spent={spent(p)}
								busy={busy === p.id}
								locked={busy !== ''}
								onrun={(ev) => run(p, ev)}
							/>
						{/each}
					</div>
				</section>
			{/if}
		{/each}
	</div>
	<Popover {anchor} bind:open={pickOpen}>
		<div class="pickbox">
			<span class="muted faint">{t('run.phases.runOn')}</span>
			{#each open as mr (mr.repo + mr.branch)}
				<button class="btn-sm" onclick={() => start(pick, mr.repo)}
					>{mr.repo === 'repo' ? mr.branch : `${mr.repo}: ${mr.branch}`}</button
				>
			{/each}
		</div>
	</Popover>
{:else if error}
	{@render failure()}
{/if}

{#snippet failure()}
	<ErrorCard {cause} detail={error} onretry={load} ondismiss={() => (error = '')} />
{/snippet}

<style>
	.pc,
	section {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.pc {
		gap: 16px;
	}
	.mrs {
		display: flex;
		gap: 6px 16px;
		align-items: baseline;
		flex-wrap: wrap;
		padding: 8px 12px;
		font-size: var(--fs-label);
	}
	.mr {
		display: inline-flex;
		gap: 8px;
		align-items: baseline;
		flex-wrap: wrap;
		min-width: 0;
	}
	.pickbox {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px;
		min-width: 200px;
	}
</style>
