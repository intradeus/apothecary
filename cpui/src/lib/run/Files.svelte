<script lang="ts">
	// Browse the run's checkout as it is right now. Read-only: the agent owns
	// the files while it works; editing is what "Open editor" is for.
	import { api } from '$lib/api';
	import type { WorkspaceEntry, WorkspaceFile } from '$lib/domain';
	import { onMount } from 'svelte';

	let { runId }: { runId: string } = $props();

	let dirs = $state<Record<string, WorkspaceEntry[]>>({});
	let open = $state<Record<string, boolean>>({ '': true });
	let error = $state('');
	let selected = $state('');
	let file = $state<WorkspaceFile | null>(null);
	let fileError = $state('');
	let showIgnored = $state(false);

	async function load(path: string) {
		try {
			dirs = { ...dirs, [path]: await api.workspace.tree(runId, path) };
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	function toggle(e: WorkspaceEntry) {
		const next = !open[e.path];
		open = { ...open, [e.path]: next };
		if (next && !dirs[e.path]) load(e.path);
	}

	async function view(path: string) {
		selected = path;
		file = null;
		fileError = '';
		try {
			file = await api.workspace.file(runId, path);
		} catch (e) {
			fileError = e instanceof Error ? e.message : String(e);
		}
	}

	async function reload() {
		const paths = Object.keys(dirs);
		await Promise.all(paths.map(load));
		if (selected) view(selected);
	}

	onMount(() => load(''));

	type Row = { entry: WorkspaceEntry; depth: number };
	/** The visible tree, flattened in display order. */
	const rows = $derived.by(() => {
		const out: Row[] = [];
		const walk = (path: string, depth: number) => {
			for (const e of dirs[path] ?? []) {
				if (e.ignored && !showIgnored) continue;
				out.push({ entry: e, depth });
				if (e.dir && open[e.path]) walk(e.path, depth + 1);
			}
		};
		walk('', 0);
		return out;
	});
	const lines = $derived(file && !file.binary ? file.content.split('\n') : []);
	const fmtSize = (n?: number) =>
		n === undefined
			? ''
			: n < 1024
				? `${n} B`
				: n < 1 << 20
					? `${(n / 1024).toFixed(1)} KB`
					: `${(n / (1 << 20)).toFixed(1)} MB`;
</script>

{#if error && !dirs['']}
	<div class="muted small">Files are unavailable: {error}</div>
{:else}
	<div class="split">
		<div class="tree">
			<div class="tree-head small muted">
				<label
					><input type="checkbox" bind:checked={showIgnored} style="width:auto" /> ignored</label
				>
				<button class="linklike" onclick={reload}>refresh</button>
			</div>
			{#if !dirs['']}<div class="muted small" style="padding:8px">Loading…</div>{/if}
			{#each rows as r (r.entry.path)}
				<button
					class="node"
					class:sel={r.entry.path === selected}
					class:ignored={r.entry.ignored}
					style="padding-left:{8 + r.depth * 14}px"
					onclick={() => (r.entry.dir ? toggle(r.entry) : view(r.entry.path))}
					title={r.entry.path}
				>
					<span class="twisty">{r.entry.dir ? (open[r.entry.path] ? '▾' : '▸') : ''}</span>
					<span class="mono name">{r.entry.name}</span>
				</button>
			{/each}
		</div>
		<div class="viewer">
			{#if !selected}
				<div class="muted small" style="padding:12px">Pick a file.</div>
			{:else}
				<div class="small muted viewer-head">
					<span class="mono">{selected}</span>
					{#if file}· {fmtSize(file.size)}{file.truncated ? ' · first 1 MB shown' : ''}{/if}
				</div>
				{#if fileError}
					<div class="muted small" style="padding:12px">{fileError}</div>
				{:else if !file}
					<div class="muted small" style="padding:12px">Loading…</div>
				{:else if file.binary}
					<div class="muted small" style="padding:12px">Binary file.</div>
				{:else}
					<table class="mono">
						<tbody>
							{#each lines as l, i (i)}
								<tr><td class="ln">{i + 1}</td><td class="code">{l}</td></tr>
							{/each}
						</tbody>
					</table>
				{/if}
			{/if}
		</div>
	</div>
{/if}

<style>
	.split {
		display: grid;
		grid-template-columns: minmax(200px, 30%) 1fr;
		gap: 10px;
		min-height: 300px;
	}
	.tree,
	.viewer {
		border: 1px solid var(--border);
		border-radius: 8px;
		overflow: auto;
		max-height: 70vh;
	}
	.viewer {
		background: var(--bg);
	}
	.tree-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 4px 8px;
		border-bottom: 1px solid var(--border);
	}
	.tree-head label {
		display: flex;
		gap: 4px;
		align-items: center;
		margin: 0;
	}
	.linklike {
		background: none;
		border: none;
		padding: 0;
		color: var(--accent);
		cursor: pointer;
		font: inherit;
	}
	.node {
		display: flex;
		gap: 4px;
		align-items: center;
		width: 100%;
		background: none;
		border: none;
		border-radius: 0;
		color: var(--text);
		padding-top: 2px;
		padding-bottom: 2px;
		cursor: pointer;
		text-align: left;
		font: inherit;
	}
	.node:hover {
		background: var(--panel2);
	}
	.node.sel {
		background: color-mix(in srgb, var(--accent) 18%, transparent);
	}
	.node.ignored {
		opacity: 0.5;
	}
	.twisty {
		width: 10px;
		font-size: 10px;
		color: var(--muted);
	}
	.name {
		white-space: nowrap;
	}
	.viewer-head {
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
</style>
