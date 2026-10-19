<script lang="ts">
	// What the run changed (DESIGN §6 Changes): every file that differs from
	// where its branch forked off the source branch — committed and not — and
	// the selected file's diff. Refreshes every 5s while the run works and the
	// window is on screen.
	import { api } from '$lib/api';
	import type { ChangedFile, FileDiff, WorkspaceChanges } from '$lib/domain';
	import { onMount } from 'svelte';
	import { parseUnified } from '$lib/diff';
	import { GIT_HUE, hueOf } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';
	import CodeView from '$lib/ui/CodeView.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';

	let {
		runId,
		active,
		repo = '',
		visible = true,
		oncount
	}: {
		runId: string;
		active: boolean;
		repo?: string;
		visible?: boolean;
		oncount?: (n: number) => void;
	} = $props();

	let changes = $state<WorkspaceChanges | null>(null);
	let error = $state('');
	let selected = $state('');
	let diff = $state<FileDiff | null>(null);
	let diffError = $state('');
	let loadingDiff = $state(false);
	let showCommits = $state(false);

	async function refresh() {
		try {
			changes = await api.workspace.changes(runId, repo);
			error = '';
			oncount?.(changes.files.length);
			if (!selected && changes.files.length) select(changes.files[0].path);
			else if (selected) loadDiff(selected, true);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function loadDiff(path: string, quiet = false) {
		if (!quiet) loadingDiff = true;
		try {
			const d = await api.workspace.diff(runId, path, repo);
			if (path === selected) {
				diff = d;
				diffError = '';
			}
		} catch (e) {
			if (path === selected) diffError = e instanceof Error ? e.message : String(e);
		} finally {
			loadingDiff = false;
		}
	}

	function select(path: string) {
		selected = path;
		diff = null;
		loadDiff(path);
	}

	onMount(() => {
		refresh();
		const t = setInterval(() => {
			if (active && visible && !document.hidden) refresh();
		}, 5000);
		return () => clearInterval(t);
	});
	// Catch up when the window comes back on screen.
	let wasVisible = true; // mounted the first time it is shown
	$effect(() => {
		if (visible && !wasVisible) refresh();
		wasVisible = visible;
	});

	const totals = $derived.by(() => {
		let add = 0,
			del = 0;
		for (const f of changes?.files ?? []) {
			add += f.additions;
			del += f.deletions;
		}
		return { add, del };
	});

	const statusLabel: Record<string, string> = {
		A: 'added',
		M: 'modified',
		D: 'deleted',
		R: 'renamed',
		C: 'copied',
		'?': 'new, untracked'
	};
	const lines = $derived(diff ? parseUnified(diff.diff) : []);
	const file = $derived<ChangedFile | undefined>(changes?.files.find((f) => f.path === selected));
</script>

{#if error && !changes}
	<div class="pad">
		<ErrorCard cause="Changes are unavailable." detail={error} onretry={refresh} />
	</div>
{:else if !changes}
	<Loading text="Reading the branch…" />
{:else}
	<div class="view">
		<div class="vbar sum">
			<span
				>vs <span class="mono tx">{(changes.base || 'HEAD').replace(/^origin\//, '')}</span></span
			>
			{#if changes.mergeBase}<span class="mono fnt">fork {changes.mergeBase.slice(0, 7)}</span>{/if}
			<span class="tx">{changes.files.length} file{changes.files.length === 1 ? '' : 's'}</span>
			<span class="mono"
				><span class="hue-grn">+{totals.add}</span> <span class="hue-ros">−{totals.del}</span></span
			>
			{#if changes.commits.length}
				<button
					class="linklike brand"
					aria-expanded={showCommits}
					onclick={() => (showCommits = !showCommits)}
					>{changes.commits.length} commit{changes.commits.length === 1 ? '' : 's'}
					{showCommits ? I.menu : I.caret}</button
				>
			{/if}
			<span class="spacer"></span>
			<button class="btn-icon" onclick={refresh} title="Refresh now" aria-label="Refresh"
				>{I.retry}</button
			>
		</div>
		{#if showCommits}
			<div class="commits">
				{#each changes.commits as c (c.sha)}
					<div><span class="mono fnt">{c.sha.slice(0, 7)}</span> {c.subject}</div>
				{/each}
			</div>
		{/if}
		{#if changes.files.length === 0}
			<div class="empty-line">No changes yet.</div>
		{:else}
			<div class="split">
				<div class="files">
					{#each changes.files as f (f.path)}
						<button
							class="rowbtn file"
							class:sel={f.path === selected}
							onclick={() => select(f.path)}
						>
							<span class="st mono hue-{hueOf(GIT_HUE, f.status)}" title={statusLabel[f.status]}
								>{f.status === '?' ? 'U' : f.status}</span
							>
							<span class="path mono" title={f.oldPath ? `${f.oldPath} → ${f.path}` : f.path}
								>{f.path}</span
							>
							<span class="wip hue-amb" title={f.committed ? '' : 'Not committed yet'}
								>{f.committed ? '' : I.dot}</span
							>
							<span class="cnt mono">
								{#if f.binary}<span class="fnt">bin</span>{:else}<span class="hue-grn"
										>+{f.additions}</span
									>
									<span class="hue-ros">−{f.deletions}</span>{/if}
							</span>
						</button>
					{/each}
				</div>
				<div class="dv">
					{#if file}
						<div class="dh mono">
							{file.oldPath ? `${file.oldPath} → ` : ''}{file.path}
							<span class="fnt"
								>· {file.status === '?' ? 'U' : file.status}{file.binary
									? ''
									: ` · +${file.additions} −${file.deletions}`}{file.committed
									? ''
									: ' · uncommitted'}</span
							>
						</div>
					{/if}
					{#if diffError}
						<ErrorCard
							cause="The diff could not be read."
							detail={diffError}
							onretry={() => loadDiff(selected)}
						/>
					{:else if loadingDiff && !diff}
						<Loading text="Loading diff…" />
					{:else if diff}
						<CodeView
							{lines}
							gutters={2}
							binary={file?.binary ? 'Binary file' : undefined}
							truncated={diff.truncated ? 'the rest is in the editor' : undefined}
						/>
					{/if}
				</div>
			</div>
		{/if}
	</div>
{/if}

<style>
	.pad {
		padding: 12px;
	}
	.sum {
		gap: 4px 14px;
		font-size: var(--fs-label);
		color: var(--mut);
	}
	.commits {
		margin: 0 8px 8px;
		padding: 6px 10px;
		border-radius: var(--r-row);
		background: var(--p1);
		font-size: var(--fs-label);
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.split {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 0 8px 10px;
	}
	.files {
		display: flex;
		flex-direction: column;
		font-size: var(--fs-label);
		padding: 4px;
		border-radius: var(--r-card);
		background: var(--p2);
	}
	.file {
		display: grid;
		grid-template-columns: 18px minmax(0, 1fr) 10px 80px;
		padding: 3px 8px;
		border-radius: var(--r-icon);
	}
	.st {
		font-size: var(--fs-meta);
		font-weight: 600;
	}
	.path {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		direction: rtl;
		text-align: left;
	}
	.wip {
		font-size: var(--fs-badge);
	}
	.cnt {
		text-align: right;
		font-size: var(--fs-meta);
	}
	.dv {
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.dh {
		padding: 4px 4px 6px;
		overflow-wrap: anywhere;
	}
	.dh .fnt {
		font-weight: 400;
	}
	/* Side by side once the window is wide enough for a file list beside a
	 * readable diff: a container query on the window, not the viewport, so
	 * it has its own threshold. */
	@container (min-width: 820px) {
		.split {
			display: grid;
			grid-template-columns: minmax(240px, 34%) minmax(0, 1fr);
			align-items: start;
		}
		.files {
			position: sticky;
			top: 0;
		}
	}
</style>
