<script lang="ts">
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
			title: 'Clear the build cache?',
			body: `The devcontainer image layers this workshop’s runs reuse are deleted (${fmtBytes(buildCache?.bytes ?? 0)}). No <b>run</b> or <b>workspace</b> is touched. The next run rebuilds the image from scratch, which is slow; runs after that are fast again.`,
			action: 'Clear cache'
		});
		if (!ok) return;
		clearingCache = true;
		cacheError = '';
		try {
			await api.projects.clearBuildCache(project.id);
			await loadBuildCache();
			toast('Build cache cleared');
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
		const t = setInterval(() => (now = Date.now()), 1000);
		return () => {
			clearInterval(checkTimer);
			clearInterval(t);
		};
	});

	const running = $derived(check?.status === 'running');
	const STATUS: Record<EnvCheck['status'], { label: string; tone: CheckTone }> = {
		running: { label: 'running', tone: 'live' },
		pass: { label: 'all good', tone: 'ok' },
		fail: { label: 'failed', tone: 'bad' },
		cancelled: { label: 'cancelled', tone: 'none' }
	};
	const checkSteps = $derived<Step[]>(
		(check?.steps ?? []).map((s, i) => ({
			key: `${i}:${s.id}`,
			tone: s.status === 'pass' ? 'ok' : s.status === 'fail' ? 'bad' : 'mut',
			label: s.id,
			detail: s.status === 'skip' ? `skipped${s.detail ? `: ${s.detail}` : ''}` : (s.detail ?? ''),
			bad: s.status === 'fail'
		}))
	);
	const preflightSteps = $derived<Step[]>(
		preflight && preflight !== 'running'
			? preflight.steps.map((s) => ({
					key: s.id,
					tone: s.ok ? 'ok' : 'bad',
					label: s.id,
					detail: s.ok ? '' : (s.error ?? 'failed'),
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
			<h2>Envcheck</h2>
			{#if check}
				<CheckChip tone={STATUS[check.status].tone} label={STATUS[check.status].label} />
				{#if check.tearingDown}<CheckChip tone="live" label="removing the workspace" />{/if}
			{/if}
			<span class="spacer"></span>
			<Toggle
				bind:checked={checkKeep}
				label="keep workspace"
				title="Leave the throwaway workspace up afterwards, to poke at it"
			/>
			{#if running}
				<button class="btn-sm" aria-busy={cancelling} onclick={cancelEnvCheck}
					>{I.stop} Cancel</button
				>
				<button
					class="btn-sm"
					aria-busy={starting}
					title="Start over: cancels the running check (it cleans up after itself)"
					onclick={startEnvCheck}>{I.retry} Restart</button
				>
			{:else}
				<button
					class="btn-sm btn-sel"
					aria-busy={starting}
					title="Provision a throwaway workspace and prove the contract"
					onclick={startEnvCheck}
					>{check ? `${I.retry} Run again` : `${I.play} Run envcheck`}</button
				>
			{/if}
		</div>
		<div class="pane-body col">
			<p class="lede">
				Provisions a throwaway workspace and proves the derived contract: image, firewall, egress,
				harness, shim, tunnel. Then destroys it, unless you keep it.
			</p>
			{#if checkError}<ErrorCard cause="Envcheck request failed" detail={checkError} />{/if}
			{#if !checkLoaded}
				<Skeleton lines={3} />
			{:else if check}
				<div class="meta">
					{#if validDate(check.startedAt)}<span title={fmtAbs(check.startedAt)}
							>started {ago(check.startedAt)}</span
						>{/if}
					<span class="mono">{elapsed}</span>
					{#if check.keep}<span>workspace kept</span>{/if}
					<span class="mono fnt">{check.id.slice(-8).toLowerCase()}</span>
				</div>
				<StepList
					steps={checkSteps}
					live={running ? { label: 'next step', detail: 'working…' } : null}
					empty="No steps were reported."
				/>
			{:else}
				<EmptyState
					inline
					title="Never checked"
					text="since the engine started. Run one before the first real run."
				/>
			{/if}
		</div>
	</section>

	<!-- Preflight -->
	<section class="c pane">
		<div class="pane-head">
			<h2>Preflight</h2>
			{#if preflight && preflight !== 'running'}
				<CheckChip
					tone={preflight.ok ? 'ok' : 'bad'}
					label={preflight.ok ? 'all good' : 'failed'}
				/>
			{/if}
			<span class="spacer"></span>
			<button
				class="btn-sm"
				onclick={runPreflight}
				aria-busy={preflight === 'running'}
				disabled={preflight === 'running'}
				>{preflight === 'running'
					? 'Checking…'
					: preflight
						? `${I.retry} Check again`
						: `${I.play} Run preflight`}</button
			>
		</div>
		<div class="pane-body col">
			<p class="lede">
				Fast checks of the configuration this workshop names — its integrations, commands and
				default flow — without provisioning anything. Every run is checked the same way when it is
				created.
			</p>
			{#if preflight === 'running'}
				<Skeleton lines={3} />
			{:else if preflight}
				<div class="meta">
					<span>checked {ago(preflightAt ?? now)}</span>
					<span>{preflight.steps.filter((s) => s.ok).length}/{preflight.steps.length} ok</span>
				</div>
				<StepList steps={preflightSteps} />
			{:else}
				<EmptyState inline title="Not run yet" text="on this page." />
			{/if}
		</div>
	</section>

	<!-- Build cache -->
	<section class="c pane">
		<div class="pane-head">
			<h2>Build cache</h2>
			{#if buildCache?.available}<span class="pill t-grn">kept</span>{/if}
			<span class="spacer"></span>
			{#if buildCache?.available}
				<button
					class="btn-danger btn-sm"
					onclick={clearBuildCache}
					aria-busy={clearingCache}
					disabled={clearingCache || !buildCache.bytes}
					>{clearingCache ? 'Clearing…' : 'Clear…'}</button
				>
			{/if}
		</div>
		<div class="pane-body col">
			{#if cacheError}<ErrorCard
					cause="Build cache request failed"
					detail={cacheError}
					onretry={loadBuildCache}
				/>{/if}
			{#if !cacheLoaded}
				<Skeleton lines={2} />
			{:else if buildCache?.available}
				<div class="row size">
					<span class="mono big">{buildCache.bytes ? fmtBytes(buildCache.bytes) : 'empty'}</span>
					<span class="mut small"
						>{buildCache.bytes
							? 'of image layers, restored by every run after the first'
							: 'the next run builds the image and fills it'}</span
					>
				</div>
				<p class="lede">
					Clear it when a build starts from something stale — a moved base image, a bad layer. The
					next run rebuilds and refills it.
				</p>
			{:else if !cacheError}
				<EmptyState
					inline
					title="No cache here"
					text="This provisioner keeps no build cache of its own."
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
