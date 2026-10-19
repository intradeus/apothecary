<script lang="ts">
	// What the run changed (DESIGN §6 Changes): every file that differs from
	// where its branch forked off the source branch — committed and not — and
	// the selected file's diff. Refreshes every 5s while the run works and the
	// window is on screen.
	import { api } from '$lib/api';
	import type { ChangedFile, FileDiff, WorkspaceChanges } from '$lib/domain';
	import { onMount } from 'svelte';

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
	const stHue: Record<string, string> = {
		A: 'grn',
		M: 'amb',
		D: 'ros',
		R: 'vio',
		C: 'tea',
		'?': 'mut'
	};

	type Line = { cls: string; old?: number; new?: number; text: string };
	/** Unified diff → rows with old/new line numbers. */
	function parse(d: string): Line[] {
		const out: Line[] = [];
		let o = 0,
			n = 0;
		for (const raw of d.split('\n')) {
			if (
				raw.startsWith('diff --git') ||
				raw.startsWith('index ') ||
				raw.startsWith('new file') ||
				raw.startsWith('deleted file') ||
				raw.startsWith('similarity') ||
				raw.startsWith('rename ') ||
				raw.startsWith('--- ') ||
				raw.startsWith('+++ ')
			)
				continue;
			const h = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/.exec(raw);
			if (h) {
				o = Number(h[1]);
				n = Number(h[2]);
				out.push({ cls: 'hunk', text: raw });
			} else if (raw.startsWith('+')) {
				out.push({ cls: 'add', new: n++, text: raw.slice(1) });
			} else if (raw.startsWith('-')) {
				out.push({ cls: 'del', old: o++, text: raw.slice(1) });
			} else if (raw.startsWith('\\')) {
				out.push({ cls: 'meta', text: raw });
			} else if (raw.length || out.length) {
				out.push({ cls: 'ctx', old: o++, new: n++, text: raw.slice(1) });
			}
		}
		while (out.length && out[out.length - 1].cls === 'ctx' && !out[out.length - 1].text) out.pop();
		return out;
	}
	const lines = $derived(diff ? parse(diff.diff) : []);
	const file = $derived<ChangedFile | undefined>(changes?.files.find((f) => f.path === selected));
</script>

{#if error && !changes}
	<div class="msg">Changes are unavailable: {error}</div>
{:else if !changes}
	<div class="msg"><span class="d pulse"></span>Reading the branch…</div>
{:else}
	<div class="ch">
		<div class="sum">
			<span
				>vs <span class="mono tx">{(changes.base || 'HEAD').replace(/^origin\//, '')}</span></span
			>
			{#if changes.mergeBase}<span class="mono sm">fork {changes.mergeBase.slice(0, 7)}</span>{/if}
			<span class="tx">{changes.files.length} file{changes.files.length === 1 ? '' : 's'}</span>
			<span class="mono nums"
				><span class="hue-grn">+{totals.add}</span> <span class="hue-ros">−{totals.del}</span></span
			>
			{#if changes.commits.length}
				<button class="linklike cm" onclick={() => (showCommits = !showCommits)}
					>{changes.commits.length} commit{changes.commits.length === 1 ? '' : 's'}
					{showCommits ? '▾' : '▸'}</button
				>
			{/if}
			<button class="linklike rf" onclick={refresh} title="Refresh now">↻</button>
		</div>
		{#if showCommits}
			<div class="commits">
				{#each changes.commits as c (c.sha)}
					<div><span class="mono fnt">{c.sha.slice(0, 7)}</span> {c.subject}</div>
				{/each}
			</div>
		{/if}
		{#if changes.files.length === 0}
			<div class="msg">No changes yet.</div>
		{:else}
			<div class="split">
				<div class="files">
					{#each changes.files as f (f.path)}
						<button class="file" class:sel={f.path === selected} onclick={() => select(f.path)}>
							<span class="st hue-{stHue[f.status]}" title={statusLabel[f.status]}
								>{f.status === '?' ? 'U' : f.status}</span
							>
							<span class="path" title={f.oldPath ? `${f.oldPath} → ${f.path}` : f.path}
								>{f.path}</span
							>
							<span class="wip" title={f.committed ? '' : 'Not committed yet'}
								>{f.committed ? '' : '●'}</span
							>
							<span class="cnt">
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
						<div class="dh">
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
						<div class="msg">{diffError}</div>
					{:else if loadingDiff && !diff}
						<div class="msg"><span class="d pulse"></span>Loading diff…</div>
					{:else if diff && file?.binary}
						<div class="bin">Binary file</div>
					{:else if diff}
						<div class="code">
							{#each lines as l, i (i)}
								<div class="dl {l.cls}">
									<span class="ln">{l.old ?? ''}</span><span class="ln">{l.new ?? ''}</span><b
										>{l.cls === 'add' ? '+' : l.cls === 'del' ? '−' : ''}</b
									><span class="tx2">{l.text}</span>
								</div>
							{/each}
							{#if diff.truncated}<div class="trunc">
									truncated — the rest is in the editor
								</div>{/if}
						</div>
					{/if}
				</div>
			</div>
		{/if}
	</div>
{/if}

<style>
	.msg {
		color: var(--mut);
		font-size: 12px;
		padding: 10px 14px;
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.d {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--az);
	}
	.ch {
		flex: 1;
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
	}
	.sum {
		padding: 10px 12px 8px;
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
		font-size: 12px;
		align-items: baseline;
		color: var(--mut);
	}
	.tx {
		color: var(--tx);
	}
	.sm {
		font-size: 11px;
	}
	.nums {
		font-weight: 500;
	}
	.cm,
	.rf {
		color: var(--brand);
		font-size: 12px;
		font-weight: 500;
	}
	.rf {
		margin-left: auto;
	}
	.commits {
		margin: 0 8px 8px;
		padding: 6px 10px;
		border-radius: 10px;
		background: var(--p1);
		font-size: 12px;
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
		font-size: 12px;
		padding: 4px;
		border-radius: 13px;
		background: var(--p2);
	}
	.file {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		display: grid;
		grid-template-columns: 18px minmax(0, 1fr) 10px 80px;
		gap: 8px;
		padding: 3px 8px;
		border-radius: 7px;
		align-items: center;
	}
	.file:hover {
		background: var(--p1);
	}
	.file.sel {
		background: var(--p3);
	}
	.st {
		font-family: var(--mono);
		font-size: 11px;
		font-weight: 600;
	}
	.hue-mut {
		color: var(--mut);
	}
	.path {
		font-family: var(--mono);
		font-size: 11.5px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		direction: rtl;
		text-align: left;
	}
	.wip {
		color: var(--amb);
		font-size: 8px;
	}
	.cnt {
		text-align: right;
		font-family: var(--mono);
		font-size: 11px;
	}
	.dv {
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.dh {
		padding: 4px 4px 6px;
		font-family: var(--mono);
		font-size: 11.5px;
		font-weight: 500;
		overflow-wrap: anywhere;
	}
	.dh .fnt {
		font-weight: 400;
	}
	.bin {
		padding: 30px;
		text-align: center;
		color: var(--mut);
		background: var(--code);
		border-radius: 13px;
	}
	.code {
		font-family: var(--mono);
		font-size: 11px;
		line-height: 1.65;
		border-radius: 13px;
		overflow: auto;
		background: var(--code);
	}
	.dl {
		display: grid;
		grid-template-columns: 34px 34px 14px 1fr;
		min-width: max-content;
	}
	.dl .ln {
		text-align: right;
		padding-right: 6px;
		opacity: 0.45;
		user-select: none;
	}
	.dl b {
		font-weight: 600;
	}
	.tx2 {
		white-space: pre;
	}
	.dl.add {
		background: var(--grnBg);
		color: var(--grn);
	}
	.dl.del {
		background: var(--rosBg);
		color: var(--ros);
	}
	.dl.hunk {
		color: var(--az);
		opacity: 0.8;
		display: block;
		padding: 2px 8px;
	}
	.dl.meta {
		color: var(--mut);
		display: block;
		padding: 0 8px;
	}
	.trunc {
		padding: 6px 10px;
		color: var(--mut);
		font-family: var(--sans);
	}
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
