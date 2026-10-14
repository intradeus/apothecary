<script lang="ts">
	// What the run changed: every file that differs from where its branch
	// forked off the source branch — committed and not — with a diff per file.
	// Refreshes on its own while the run is working.
	import { api } from '$lib/api';
	import type { ChangedFile, FileDiff, WorkspaceChanges } from '$lib/domain';
	import { onMount } from 'svelte';

	let { runId, active }: { runId: string; active: boolean } = $props();

	let changes = $state<WorkspaceChanges | null>(null);
	let error = $state('');
	let selected = $state('');
	let diff = $state<FileDiff | null>(null);
	let diffError = $state('');
	let loadingDiff = $state(false);

	async function refresh() {
		try {
			changes = await api.workspace.changes(runId);
			error = '';
			if (!selected && changes.files.length) select(changes.files[0].path);
			else if (selected) loadDiff(selected, true);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function loadDiff(path: string, quiet = false) {
		if (!quiet) loadingDiff = true;
		try {
			const d = await api.workspace.diff(runId, path);
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
			if (active && !document.hidden) refresh();
		}, 5000);
		return () => clearInterval(t);
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

{#if error}
	<div class="muted small">Changes are unavailable: {error}</div>
{:else if !changes}
	<div class="muted small">Loading changes…</div>
{:else}
	<div class="head small muted">
		<span class="mono">{changes.branch}</span>
		against <span class="mono">{changes.base || 'HEAD'}</span>
		{#if changes.mergeBase}(fork point <span class="mono">{changes.mergeBase.slice(0, 8)}</span
			>){/if}
		· {changes.files.length} file{changes.files.length === 1 ? '' : 's'}
		<span class="add">+{totals.add}</span>
		<span class="del">−{totals.del}</span>
		{#if changes.commits.length}· {changes.commits.length} commit{changes.commits.length === 1
				? ''
				: 's'}{/if}
		<button class="linklike" onclick={refresh}>refresh</button>
	</div>
	{#if changes.commits.length}
		<details class="commits small">
			<summary class="muted">Commits on this branch</summary>
			{#each changes.commits as c (c.sha)}
				<div><span class="mono muted">{c.sha.slice(0, 8)}</span> {c.subject}</div>
			{/each}
		</details>
	{/if}
	{#if changes.files.length === 0}
		<div class="muted small" style="margin-top:12px">No changes yet.</div>
	{:else}
		<div class="split">
			<div class="files">
				{#each changes.files as f (f.path)}
					<button class="file" class:sel={f.path === selected} onclick={() => select(f.path)}>
						<span class="st st-{f.status === '?' ? 'u' : f.status}" title={statusLabel[f.status]}
							>{f.status === '?' ? 'U' : f.status}</span
						>
						<span class="mono path" title={f.oldPath ? `${f.oldPath} → ${f.path}` : f.path}
							>{f.path}</span
						>
						<span class="counts mono">
							{#if f.binary}bin{:else}<span class="add">+{f.additions}</span>
								<span class="del">−{f.deletions}</span>{/if}
						</span>
						{#if !f.committed}<span class="wip" title="Not committed yet">●</span>{/if}
					</button>
				{/each}
			</div>
			<div class="diff">
				{#if file}
					<div class="small muted diff-head">
						<span class="mono">{file.oldPath ? `${file.oldPath} → ` : ''}{file.path}</span>
						· {statusLabel[file.status]}{file.committed ? '' : ' · uncommitted'}
					</div>
				{/if}
				{#if diffError}
					<div class="muted small">{diffError}</div>
				{:else if loadingDiff && !diff}
					<div class="muted small">Loading diff…</div>
				{:else if diff && file?.binary}
					<div class="muted small">Binary file — no text diff.</div>
				{:else if diff}
					<table class="mono">
						<tbody>
							{#each lines as l, i (i)}
								<tr class={l.cls}>
									<td class="ln">{l.old ?? ''}</td>
									<td class="ln">{l.new ?? ''}</td>
									<td class="code">{l.cls === 'add' ? '+' : l.cls === 'del' ? '−' : ' '}{l.text}</td
									>
								</tr>
							{/each}
						</tbody>
					</table>
					{#if diff.truncated}<div class="muted small">… diff truncated</div>{/if}
				{/if}
			</div>
		</div>
	{/if}
{/if}

<style>
	.head {
		display: flex;
		gap: 6px;
		align-items: center;
		flex-wrap: wrap;
	}
	.add {
		color: var(--green);
	}
	.del {
		color: var(--red);
	}
	.linklike {
		background: none;
		border: none;
		padding: 0;
		color: var(--accent);
		cursor: pointer;
		font: inherit;
		margin-left: auto;
	}
	.commits {
		margin: 6px 0;
	}
	.split {
		display: grid;
		grid-template-columns: minmax(200px, 30%) 1fr;
		gap: 10px;
		margin-top: 10px;
		min-height: 300px;
	}
	.files {
		border: 1px solid var(--border);
		border-radius: 8px;
		overflow: auto;
		max-height: 68vh;
	}
	.file {
		display: flex;
		gap: 6px;
		align-items: center;
		width: 100%;
		background: none;
		border: none;
		border-bottom: 1px solid var(--border);
		color: var(--text);
		padding: 5px 8px;
		cursor: pointer;
		text-align: left;
		font: inherit;
		border-radius: 0;
	}
	.file:hover {
		background: var(--panel2);
	}
	.file.sel {
		background: color-mix(in srgb, var(--accent) 18%, transparent);
	}
	.path {
		flex: 1;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		direction: rtl;
		text-align: left;
	}
	.counts {
		font-size: 11px;
		white-space: nowrap;
	}
	.st {
		font-size: 10px;
		font-weight: 700;
		width: 14px;
		text-align: center;
	}
	.st-A,
	.st-u {
		color: var(--green);
	}
	.st-M,
	.st-R,
	.st-C {
		color: var(--amber);
	}
	.st-D {
		color: var(--red);
	}
	.wip {
		color: var(--blue);
		font-size: 9px;
	}
	.diff {
		border: 1px solid var(--border);
		border-radius: 8px;
		overflow: auto;
		max-height: 68vh;
		background: var(--bg);
	}
	.diff-head {
		padding: 6px 10px;
		border-bottom: 1px solid var(--border);
		position: sticky;
		top: 0;
		background: var(--panel);
	}
	table {
		border-collapse: collapse;
		width: 100%;
		font-size: 12px;
	}
	td {
		padding: 0 8px;
		vertical-align: top;
	}
	.ln {
		width: 1%;
		color: var(--muted);
		text-align: right;
		user-select: none;
		opacity: 0.6;
	}
	.code {
		white-space: pre-wrap;
		word-break: break-all;
	}
	tr.add {
		background: color-mix(in srgb, var(--green) 12%, transparent);
	}
	tr.del {
		background: color-mix(in srgb, var(--red) 12%, transparent);
	}
	tr.hunk td {
		color: var(--blue);
		background: var(--panel2);
		padding: 2px 8px;
	}
	tr.meta td {
		color: var(--muted);
	}
</style>
