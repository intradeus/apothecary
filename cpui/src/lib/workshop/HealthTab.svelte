<script lang="ts">
	import { t, th } from '$lib/i18n/index.svelte';
	// Workshop health tab (DESIGN 7.7): preflight (config checks), envcheck
	// (a throwaway workspace that proves the contract) and the build cache.
	//
	// Opening the tab never STARTS a check: it resumes the latest one, if any,
	// and follows it while it runs.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { BuildCache, EnvCheck, Preflight, Project } from '$lib/domain';
	import { ago, fmtAbs, fmtBytes, fmtDuration } from '$lib/format';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import CheckChip, { type CheckTone } from '$lib/ui/CheckChip.svelte';
	import StepList, { type Step } from '$lib/ui/StepList.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';
	import TransportIcon from '$lib/ui/TransportIcon.svelte';

	let { project }: { project: Project } = $props();

	// --- preflight -----------------------------------------------------------

	let preflight = $state<Preflight | 'running' | null>(null);
	let preflightAt = $state<number | null>(null);

	async function runPreflight() {
		preflight = 'running';
		try {
			preflight = await api.projects.preflight(project.id);
		} catch (e) {
			preflight = {
				ok: false,
				steps: [{ id: 'request', ok: false, error: e instanceof Error ? e.message : String(e) }]
			};
		}
		preflightAt = Date.now();
	}

	// --- envcheck --------------------------------------------------------------

	let check = $state<EnvCheck | null>(null);
	let checkLoaded = $state(false);
	let checkKeep = $state(false);
	let checkError = $state('');
	/** A start / cancel request is in flight (aria-busy on its button). */
	let starting = $state(false);
	let cancelling = $state(false);
	let checkTimer: ReturnType<typeof setInterval> | undefined;
	let now = $state(Date.now());

	// Follow a check until it is fully done (verdict AND teardown), so the
	// destroy step shows up too.
	function follow(checkId: string) {
		clearInterval(checkTimer);
		checkTimer = setInterval(async () => {
			try {
				check = await api.projects.envCheck(checkId);
				if (check.status !== 'running' && !check.tearingDown) clearInterval(checkTimer);
			} catch {
				/* keep polling */
			}
		}, 2000);
	}

	// Starting a check while one runs supersedes it: the engine cancels the old
	// one (it cleans up after itself) — no waiting it out.
	async function startEnvCheck() {
		checkError = '';
		starting = true;
		try {
			const checkId = await api.projects.startEnvCheck(project.id, checkKeep);
			check = await api.projects.envCheck(checkId);
			follow(checkId);
		} catch (e) {
			checkError = e instanceof Error ? e.message : String(e);
		} finally {
			starting = false;
		}
	}

	async function cancelEnvCheck() {
		if (!check) return;
		checkError = '';
		cancelling = true;
		try {
			await api.projects.cancelEnvCheck(check.id);
			check = await api.projects.envCheck(check.id);
			follow(check.id);
		} catch (e) {
			checkError = e instanceof Error ? e.message : String(e);
		} finally {
			cancelling = false;
		}
	}

	// --- build cache -------------------------------------------------------------

	// The devcontainer build cache: what makes a second run of this workshop
	// fast (the first one builds the image, every later one restores it).
	// null when this provisioner keeps none.
	let buildCache = $state<BuildCache | null>(null);
	let cacheLoaded = $state(false);
	let clearingCache = $state(false);
	let cacheError = $state('');

	async function loadBuildCache() {
		try {
			buildCache = await api.projects.buildCache(project.id);
			cacheError = '';
		} catch (e) {
			cacheError = e instanceof Error ? e.message : String(e);
		}
		cacheLoaded = true;
	}

	async function clearBuildCache() {
		const ok = await confirmDialog({
			title: t('workshop.health.confirmTitle'),
			body: th('workshop.health.confirmBody', { size: fmtBytes(buildCache?.bytes ?? 0) }),
			action: t('workshop.health.confirmAction')
		});
		if (!ok) return;
		clearingCache = true;
		cacheError = '';
		try {
			await api.projects.clearBuildCache(project.id);
			await loadBuildCache();
			toast(t('workshop.health.cleared'));
		} catch (e) {
			cacheError = e instanceof Error ? e.message : String(e);
		} finally {
			clearingCache = false;
		}
	}

	onMount(() => {
		// A check started before this page opened (or before a reload) is
		// still the workshop's check: show it, and follow it if it is live.
		api.projects
			.latestEnvCheck(project.id)
			.then((c) => {
				// An engine without this endpoint answers with the SPA shell, which
				// maps to an id-less check: that is "none", not "running".
				if (c?.id && !check) {
					check = c;
					checkKeep = c.keep;
					if (c.status === 'running' || c.tearingDown) follow(c.id);
				}
			})
			.catch((e) => (checkError = e instanceof Error ? e.message : String(e)))
			.finally(() => (checkLoaded = true));
		void loadBuildCache();
		const tick = setInterval(() => (now = Date.now()), 1000);
		return () => {
			clearInterval(checkTimer);
			clearInterval(tick);
		};
	});

	const running = $derived(check?.status === 'running');
	const STATUS: Record<EnvCheck['status'], { label: string; tone: CheckTone }> = {
		running: { label: t('workshop.health.running'), tone: 'live' },
		pass: { label: t('workshop.health.allGood'), tone: 'ok' },
		fail: { label: t('workshop.health.failed'), tone: 'bad' },
		cancelled: { label: t('workshop.health.cancelled'), tone: 'none' }
	};
	const checkSteps = $derived<Step[]>(
		(check?.steps ?? []).map((s, i) => ({
			key: `${i}:${s.id}`,
			tone: s.status === 'pass' ? 'ok' : s.status === 'fail' ? 'bad' : 'mut',
			label: s.id,
			detail:
				s.status === 'skip'
					? s.detail
						? t('workshop.health.skippedDetail', { detail: s.detail })
						: t('workshop.health.skipped')
					: (s.detail ?? ''),
			bad: s.status === 'fail'
		}))
	);
	const preflightSteps = $derived<Step[]>(
		preflight && preflight !== 'running'
			? preflight.steps.map((s) => ({
					key: s.id,
					tone: s.ok ? 'ok' : 'bad',
					label: s.id,
					detail: s.ok ? '' : (s.error ?? t('workshop.health.failed')),
					bad: !s.ok
				}))
			: []
	);
	const validDate = (s?: string) => !!s && !Number.isNaN(new Date(s).getTime());
	const elapsed = $derived(
		check && validDate(check.startedAt)
			? fmtDuration(
					(check.finishedAt ? new Date(check.finishedAt).getTime() : now) -
						new Date(check.startedAt).getTime()
				)
			: ''
	);
</script>

<div class="hl">
	<!-- Envcheck -->
	<section class="c pane wide">
		<div class="pane-head">
			<h2>{t('workshop.health.envcheck')}</h2>
			{#if check}
				<CheckChip tone={STATUS[check.status].tone} label={STATUS[check.status].label} />
				{#if check.tearingDown}<CheckChip tone="live" label={t('workshop.health.removing')} />{/if}
			{/if}
			<span class="spacer"></span>
			<Toggle
				bind:checked={checkKeep}
				label={t('workshop.health.keep')}
				title={t('workshop.health.keepTitle')}
			/>
			{#if running}
				<button class="btn-sm" aria-busy={cancelling} onclick={cancelEnvCheck}
					><TransportIcon kind="stop" /> {t('workshop.health.cancel')}</button
				>
				<button
					class="btn-sm"
					aria-busy={starting}
					title={t('workshop.health.restartTitle')}
					onclick={startEnvCheck}>{I.retry} {t('workshop.health.restart')}</button
				>
			{:else}
				<button
					class="btn-sm btn-sel"
					aria-busy={starting}
					title={t('workshop.health.runEnvcheckTitle')}
					onclick={startEnvCheck}
					>{#if check}{I.retry} {t('workshop.health.runAgain')}{:else}<TransportIcon kind="play" />
						{t('workshop.health.runEnvcheck')}{/if}</button
				>
			{/if}
		</div>
		<div class="pane-body col">
			<p class="lede">
				{t('workshop.health.envcheckLede')}
			</p>
			{#if checkError}<ErrorCard
					cause={t('workshop.health.envcheckFailed')}
					detail={checkError}
				/>{/if}
			{#if !checkLoaded}
				<Skeleton lines={3} />
			{:else if check}
				<div class="meta">
					{#if validDate(check.startedAt)}<span title={fmtAbs(check.startedAt)}
							>{t('workshop.health.started', { ago: ago(check.startedAt) })}</span
						>{/if}
					<span class="mono">{elapsed}</span>
					{#if check.keep}<span>{t('workshop.health.kept')}</span>{/if}
					<span class="mono fnt">{check.id.slice(-8).toLowerCase()}</span>
				</div>
				<StepList
					steps={checkSteps}
					live={running
						? { label: t('workshop.health.nextStep'), detail: t('workshop.health.working') }
						: null}
					empty={t('workshop.health.noSteps')}
				/>
			{:else}
				<EmptyState
					inline
					title={t('workshop.health.neverTitle')}
					text={t('workshop.health.neverText')}
				/>
			{/if}
		</div>
	</section>

	<!-- Preflight -->
	<section class="c pane">
		<div class="pane-head">
			<h2>{t('workshop.health.preflight')}</h2>
			{#if preflight && preflight !== 'running'}
				<CheckChip
					tone={preflight.ok ? 'ok' : 'bad'}
					label={preflight.ok ? t('workshop.health.allGood') : t('workshop.health.failed')}
				/>
			{/if}
			<span class="spacer"></span>
			<button
				class="btn-sm"
				onclick={runPreflight}
				aria-busy={preflight === 'running'}
				disabled={preflight === 'running'}
				>{#if preflight === 'running'}{t('workshop.health.checking')}{:else if preflight}{I.retry}
					{t('workshop.health.checkAgain')}{:else}<TransportIcon kind="play" />
					{t('workshop.health.runPreflight')}{/if}</button
			>
		</div>
		<div class="pane-body col">
			<p class="lede">
				{t('workshop.health.preflightLede')}
			</p>
			{#if preflight === 'running'}
				<Skeleton lines={3} />
			{:else if preflight}
				<div class="meta">
					<span>{t('workshop.health.checked', { ago: ago(preflightAt ?? now) })}</span>
					<span
						>{t('workshop.health.okCount', {
							ok: preflight.steps.filter((s) => s.ok).length,
							total: preflight.steps.length
						})}</span
					>
				</div>
				<StepList steps={preflightSteps} />
			{:else}
				<EmptyState
					inline
					title={t('workshop.health.notRunTitle')}
					text={t('workshop.health.notRunText')}
				/>
			{/if}
		</div>
	</section>

	<!-- Build cache -->
	<section class="c pane">
		<div class="pane-head">
			<h2>{t('workshop.health.cache')}</h2>
			{#if buildCache?.available}<span class="pill t-grn">{t('workshop.health.cacheKept')}</span
				>{/if}
			<span class="spacer"></span>
			{#if buildCache?.available}
				<button
					class="btn-danger btn-sm"
					onclick={clearBuildCache}
					aria-busy={clearingCache}
					disabled={clearingCache || !buildCache.bytes}
					>{clearingCache ? t('workshop.health.clearing') : t('workshop.health.clear')}</button
				>
			{/if}
		</div>
		<div class="pane-body col">
			{#if cacheError}<ErrorCard
					cause={t('workshop.health.cacheFailed')}
					detail={cacheError}
					onretry={loadBuildCache}
				/>{/if}
			{#if !cacheLoaded}
				<Skeleton lines={2} />
			{:else if buildCache?.available}
				<div class="row size">
					<span class="mono big"
						>{buildCache.bytes ? fmtBytes(buildCache.bytes) : t('workshop.health.cacheEmpty')}</span
					>
					<span class="mut small"
						>{buildCache.bytes
							? t('workshop.health.cacheFull')
							: t('workshop.health.cacheFill')}</span
					>
				</div>
				<p class="lede">
					{t('workshop.health.cacheLede')}
				</p>
			{:else if !cacheError}
				<EmptyState
					inline
					title={t('workshop.health.noCacheTitle')}
					text={t('workshop.health.noCacheText')}
				/>
			{/if}
		</div>
	</section>
</div>

<style>
	.hl {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		gap: 12px;
		align-items: start;
	}
	.pane-head {
		flex-wrap: wrap;
	}
	.meta {
		display: flex;
		gap: 6px 14px;
		flex-wrap: wrap;
		font-size: var(--fs-help);
		color: var(--mut);
	}
	.size {
		align-items: baseline;
	}
	.big {
		font-size: var(--fs-section);
		font-weight: 600;
	}
	@media (max-width: 1000px) {
		.hl {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
