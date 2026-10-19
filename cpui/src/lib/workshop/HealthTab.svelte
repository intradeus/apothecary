<script lang="ts">
	// Workshop health tab (DESIGN 7.7): preflight (config checks), envcheck
	// (a throwaway workspace that proves the contract) and the build cache.
	//
	// Opening the tab never STARTS a check: it resumes the latest one, if any,
	// and follows it while it runs.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { BuildCache, EnvCheck, Preflight, Project } from '$lib/domain';
	import { lore } from '$lib/lore';
	import { ago, fmtAbs, fmtBytes, fmtDuration } from '$lib/format';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';

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
		try {
			const checkId = await api.projects.startEnvCheck(project.id, checkKeep);
			check = await api.projects.envCheck(checkId);
			follow(checkId);
		} catch (e) {
			checkError = e instanceof Error ? e.message : String(e);
		}
	}

	async function cancelEnvCheck() {
		if (!check) return;
		checkError = '';
		try {
			await api.projects.cancelEnvCheck(check.id);
			check = await api.projects.envCheck(check.id);
			follow(check.id);
		} catch (e) {
			checkError = e instanceof Error ? e.message : String(e);
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
			body: `The devcontainer image layers this ${lore('workshop').toLowerCase()}’s runs reuse are deleted (${fmtBytes(buildCache?.bytes ?? 0)}). No <b>run</b> or <b>workspace</b> is touched. The next run rebuilds the image from scratch, which is slow; runs after that are fast again.`,
			action: 'Clear cache'
		});
		if (!ok) return;
		clearingCache = true;
		cacheError = '';
		try {
			await api.projects.clearBuildCache(project.id);
			await loadBuildCache();
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
	const STATUS: Record<EnvCheck['status'], { label: string; tone: string }> = {
		running: { label: 'running', tone: 't-az' },
		pass: { label: '✓ all good', tone: 't-grn' },
		fail: { label: '✕ failed', tone: 't-ros' },
		cancelled: { label: 'cancelled', tone: 't-mut' }
	};
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
				<span class="pill {STATUS[check.status].tone}"
					>{#if running}<span class="dot pulse"></span>{/if}{STATUS[check.status].label}</span
				>
				{#if check.tearingDown}<span class="pill t-amb"
						><span class="dot pulse"></span>removing the workspace</span
					>{/if}
			{/if}
			<span class="spacer"></span>
			<Toggle
				bind:checked={checkKeep}
				label="keep workspace"
				title="Leave the throwaway workspace up afterwards, to poke at it"
			/>
			{#if running}
				<button class="btn-sm" onclick={cancelEnvCheck}>■ Cancel</button>
				<button
					class="btn-sm"
					title="Start over: cancels the running check (it cleans up after itself)"
					onclick={startEnvCheck}>↻ Restart</button
				>
			{:else}
				<button
					class="btn-sm btn-sel"
					title="Provision a throwaway workspace and prove the contract"
					onclick={startEnvCheck}>{check ? '↻ Run again' : '▶ Run envcheck'}</button
				>
			{/if}
		</div>
		<div class="pane-body">
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
				<ol class="steps">
					{#each check.steps ?? [] as s, i (i)}
						<li class="st {s.status}">
							<span class="g">{s.status === 'pass' ? '✓' : s.status === 'fail' ? '✕' : '–'}</span>
							<span class="mono id">{s.id}</span>
							<span class="dt" class:bad={s.status === 'fail'}
								>{#if s.status === 'skip'}skipped{#if s.detail}: {s.detail}{/if}{:else}{s.detail ??
										''}{/if}</span
							>
						</li>
					{/each}
					{#if running}
						<li class="st live">
							<span class="g"><span class="pd pulse"></span></span>
							<span class="mono id fnt">next step</span>
							<span class="dt fnt">working…</span>
						</li>
					{/if}
					{#if !(check.steps ?? []).length && !running}
						<li class="st">
							<span class="g">–</span><span class="dt fnt">No steps were reported.</span>
						</li>
					{/if}
				</ol>
			{:else}
				<div class="none">
					<span class="serif">Never checked</span>
					<span class="mut">since the engine started. Run one before the first real run.</span>
				</div>
			{/if}
		</div>
	</section>

	<!-- Preflight -->
	<section class="c pane">
		<div class="pane-head">
			<h2>Preflight</h2>
			{#if preflight && preflight !== 'running'}
				<span class="pill {preflight.ok ? 't-grn' : 't-ros'}"
					>{preflight.ok ? '✓ all good' : '✕ failed'}</span
				>
			{/if}
			<span class="spacer"></span>
			<button class="btn-sm" onclick={runPreflight} disabled={preflight === 'running'}
				>{preflight === 'running'
					? 'Checking…'
					: preflight
						? '↻ Check again'
						: '▶ Run preflight'}</button
			>
		</div>
		<div class="pane-body">
			<p class="lede">
				Fast checks of the configuration this {lore('workshop').toLowerCase()} names — its integrations,
				commands and default flow — without provisioning anything. Every run is checked the same way when
				it is created.
			</p>
			{#if preflight === 'running'}
				<Skeleton lines={3} />
			{:else if preflight}
				<div class="meta">
					<span>checked {ago(preflightAt ?? now)}</span>
					<span>{preflight.steps.filter((s) => s.ok).length}/{preflight.steps.length} ok</span>
				</div>
				<ol class="steps">
					{#each preflight.steps as s (s.id)}
						<li class="st {s.ok ? 'pass' : 'fail'}">
							<span class="g">{s.ok ? '✓' : '✕'}</span>
							<span class="mono id">{s.id}</span>
							<span class="dt" class:bad={!s.ok}>{s.ok ? '' : (s.error ?? 'failed')}</span>
						</li>
					{/each}
				</ol>
			{:else}
				<div class="none">
					<span class="serif">Not run yet</span>
					<span class="mut">on this page.</span>
				</div>
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
					disabled={clearingCache || !buildCache.bytes}
					>{clearingCache ? 'Clearing…' : 'Clear…'}</button
				>
			{/if}
		</div>
		<div class="pane-body">
			{#if cacheError}<ErrorCard
					cause="Build cache request failed"
					detail={cacheError}
					onretry={loadBuildCache}
				/>{/if}
			{#if !cacheLoaded}
				<Skeleton lines={2} />
			{:else if buildCache?.available}
				<div class="size">
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
				<div class="none">
					<span class="serif">No cache here</span>
					<span class="mut">This provisioner keeps no build cache of its own.</span>
				</div>
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
	.wide {
		grid-column: 1 / -1;
	}
	.pane-head {
		flex-wrap: wrap;
	}
	.lede {
		margin: 0 0 10px;
		font-size: 12px;
		color: var(--mut);
		max-width: 80ch;
		text-wrap: pretty;
	}
	.meta {
		display: flex;
		gap: 6px 14px;
		flex-wrap: wrap;
		font-size: 11.5px;
		color: var(--mut);
		margin-bottom: 8px;
	}
	.steps {
		list-style: none;
		margin: 0;
		padding: 0;
		position: relative;
	}
	.st {
		position: relative;
		display: grid;
		grid-template-columns: 22px 120px minmax(0, 1fr);
		gap: 8px;
		align-items: baseline;
		padding: 5px 0;
		font-size: 12px;
	}
	.st:not(:last-child)::before {
		content: '';
		position: absolute;
		left: 10px;
		top: 22px;
		bottom: -6px;
		width: 1px;
		background: var(--ln2);
	}
	.g {
		width: 20px;
		height: 20px;
		border-radius: 50%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		font-size: 10.5px;
		font-weight: 700;
		background: var(--p2);
		border: 1px solid var(--ln);
		color: var(--mut);
		align-self: center;
	}
	.pass .g {
		background: var(--grnBg);
		border-color: var(--grnLn);
		color: var(--grn);
	}
	.fail .g {
		background: var(--rosBg);
		border-color: var(--rosLn);
		color: var(--ros);
	}
	.live .g {
		border-color: var(--azLn);
	}
	.pd {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--az);
		box-shadow: 0 0 8px var(--az);
	}
	.id {
		font-size: 11.5px;
	}
	.skip .id {
		color: var(--mut);
	}
	.dt {
		color: var(--mut);
		overflow-wrap: anywhere;
	}
	.dt.bad {
		color: var(--ros);
		font-family: var(--mono);
		font-size: 11px;
	}
	.none {
		border: 1.5px dashed var(--ln2);
		border-radius: 12px;
		padding: 10px 12px;
		display: flex;
		align-items: baseline;
		gap: 8px;
		flex-wrap: wrap;
		font-size: 12px;
	}
	.none .serif {
		font-size: 16px;
	}
	.size {
		display: flex;
		align-items: baseline;
		gap: 10px;
		margin-bottom: 8px;
		flex-wrap: wrap;
	}
	.big {
		font-size: 20px;
		font-weight: 600;
	}
	@media (max-width: 1000px) {
		.hl {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
