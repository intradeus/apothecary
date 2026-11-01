<script lang="ts">
	// What the run changed (DESIGN §6 Changes): every file that differs from
	// where its branch forked off the source branch — committed and not — and
	// the selected file's diff, the files as a list or a tree (prefs). Refreshes
	// every 5s while the run works and the window is on screen.
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import type { ChangedFile, FileDiff, WorkspaceChanges } from '$lib/domain';
	import { onMount } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { parseUnified } from '$lib/run/diff';
	import { pathTree, treeOrder, treeRows } from '$lib/core/pathTree';
	import { prefs } from '$lib/app/prefs.svelte';
	import FileLayoutSwitch from '$lib/app/FileLayoutSwitch.svelte';
	import { GIT_HUE, hueOf } from '$lib/design/hues';
	import { I } from '$lib/design/icons';
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
	/** Folded folders in the tree, by path. */
	let closed = $state<Record<string, boolean>>({});

	async function refresh() {
		try {
			changes = await api.workspace.changes(runId, repo);
			error = '';
			oncount?.(changes.files.length);
			if (!selected && changes.files.length) {
				const paths = changes.files.map((f) => f.path);
				select(prefs.fileLayout === 'tree' ? treeOrder(paths)[0] : paths[0]);
			} else if (selected) loadDiff(selected, true);
		} catch (e) {
			error = errorMessage(e);
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
			if (path === selected) diffError = errorMessage(e);
		} finally {
			if (!quiet && path === selected) loadingDiff = false;
		}
	}

	function select(path: string) {
		selected = path;
		diff = null;
		diffError = '';
		loadDiff(path);
	}

	onMount(() => {
		refresh();
		const timer = setInterval(() => {
			if (active && visible && !document.hidden) refresh();
		}, 5000);
		return () => clearInterval(timer);
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

	const statusLabel = (s: string): string | undefined => {
		switch (s) {
			case 'A':
				return t('run.changes.status.added');
			case 'M':
				return t('run.changes.status.modified');
			case 'D':
				return t('run.changes.status.deleted');
			case 'R':
				return t('run.changes.status.renamed');
			case 'C':
				return t('run.changes.status.copied');
			case '?':
				return t('run.changes.status.untracked');
		}
	};
	const lines = $derived(diff ? parseUnified(diff.diff) : []);
	const file = $derived<ChangedFile | undefined>(changes?.files.find((f) => f.path === selected));
	const byPath = $derived(new Map(changes?.files.map((f) => [f.path, f])));
	const rows = $derived(
		changes && prefs.fileLayout === 'tree'
			? treeRows(pathTree(changes.files.map((f) => f.path)), closed)
			: []
	);
</script>

{#if error && !changes}
	<div class="pad">
		<ErrorCard cause={t('run.changes.unavailable')} detail={error} onretry={refresh} />
	</div>
{:else if !changes}
	<Loading text={t('run.changes.reading')} />
{:else}
	<div class="view">
		<div class="vbar sum">
			<span
				>{t('run.changes.vs')}
				<span class="mono text-primary">{(changes.base || 'HEAD').replace(/^origin\//, '')}</span
				></span
			>
			{#if changes.mergeBase}<span class="mono faint"
					>{t('run.changes.fork', { sha: changes.mergeBase.slice(0, 7) })}</span
				>{/if}
			<span class="text-primary">{t('run.changes.files', { count: changes.files.length })}</span>
			<span class="mono"
				><span class="hue-success">+{totals.add}</span>
				<span class="hue-danger">−{totals.del}</span></span
			>
			{#if changes.commits.length}
				<button
					class="linklike brand"
					aria-expanded={showCommits}
					onclick={() => (showCommits = !showCommits)}
					>{t('run.changes.commits', { count: changes.commits.length })}
					{showCommits ? I.menu : I.caret}</button
				>
			{/if}
			<span class="spacer"></span>
			<FileLayoutSwitch />
			<button
				class="btn-icon"
				onclick={refresh}
				title={t('run.changes.refreshNow')}
				aria-label={t('run.changes.refresh')}>{I.retry}</button
			>
		</div>
		{#if showCommits}
			<div class="commits">
				{#each changes.commits as c (c.sha)}
					<div><span class="mono faint">{c.sha.slice(0, 7)}</span> {c.subject}</div>
				{/each}
			</div>
		{/if}
		{#if changes.files.length === 0}
			<div class="empty-line">{t('run.changes.none')}</div>
		{:else}
			<div class="split">
				<div class="files">
					{#if prefs.fileLayout === 'tree'}
						{#each rows as r ((r.dir ? 'd:' : 'f:') + r.path)}
							{#if r.dir}
								<button
									class="rowbtn dir"
									aria-expanded={r.open}
									title={r.path}
									onclick={() => (closed = { ...closed, [r.path]: r.open })}
								>
									<span class="name mono" style:padding-left="{r.depth * 12}px"
										>{r.open ? I.menu : I.caret}&nbsp;{r.name}/</span
									>
								</button>
							{:else}
								{@const f = byPath.get(r.path)}
								{#if f}{@render fileRow(f, r)}{/if}
							{/if}
						{/each}
					{:else}
						{#each changes.files as f (f.path)}
							{@render fileRow(f)}
						{/each}
					{/if}
				</div>
				<div class="dv">
					{#if file}
						<div class="dh mono">
							{file.oldPath ? `${file.oldPath} → ` : ''}{file.path}
							<span class="faint"
								>· {file.status === '?' ? 'U' : file.status}{file.binary
									? ''
									: ` · +${file.additions} −${file.deletions}`}{file.committed
									? ''
									: ` · ${t('run.changes.uncommitted')}`}</span
							>
						</div>
					{/if}
					{#if diffError}
						<ErrorCard
							cause={t('run.changes.diffError')}
							detail={diffError}
							onretry={() => loadDiff(selected)}
						/>
					{:else if loadingDiff && !diff}
						<Loading text={t('run.changes.loadingDiff')} />
					{:else if diff}
						<CodeView
							{lines}
							gutters={2}
							binary={file?.binary ? t('run.changes.binaryFile') : undefined}
							truncated={diff.truncated ? t('run.changes.truncated') : undefined}
						/>
					{/if}
				</div>
			</div>
		{/if}
	</div>
{/if}

<!-- A changed file: in the list by its path, in the tree by its name where
     the tree puts it. -->
{#snippet fileRow(f: ChangedFile, at?: { name: string; depth: number })}
	<button class="rowbtn file" class:on={f.path === selected} onclick={() => select(f.path)}>
		<span class="st mono hue-{hueOf(GIT_HUE, f.status)}" title={statusLabel(f.status)}
			>{f.status === '?' ? 'U' : f.status}</span
		>
		<span
			class="mono {at ? 'name' : 'path'}"
			style:padding-left={at ? `${at.depth * 12}px` : null}
			title={f.oldPath ? `${f.oldPath} → ${f.path}` : f.path}>{at ? at.name : f.path}</span
		>
		<span class="wip hue-warning" title={f.committed ? '' : t('run.changes.notCommitted')}
			>{f.committed ? '' : I.dot}</span
		>
		<span class="cnt mono">
			{#if f.binary}<span class="faint">{t('run.changes.bin')}</span>{:else}<span
					class="hue-success">+{f.additions}</span
				>
				<span class="hue-danger">−{f.deletions}</span>{/if}
		</span>
	</button>
{/snippet}

<style>
	.pad {
		padding: 12px;
	}
	.sum {
		gap: 4px 14px;
		font-size: var(--fs-label);
		color: var(--text-muted);
	}
	.commits {
		margin: 0 8px 8px;
		padding: 6px 10px;
		border-radius: var(--r-row);
		background: var(--fill-1);
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
		background: var(--fill-2);
	}
	.file,
	.dir {
		display: grid;
		grid-template-columns: 18px minmax(0, 1fr) 10px 80px;
		padding: 3px 8px;
		border-radius: var(--r-icon);
	}
	.dir .name {
		grid-column: 2 / -1;
		color: var(--text-muted);
	}
	.st {
		font-size: var(--fs-meta);
		font-weight: 600;
	}
	.path,
	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	/* A long path loses its start, not the file name. */
	.path {
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
	.dh .faint {
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
