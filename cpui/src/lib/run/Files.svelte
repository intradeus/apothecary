<script lang="ts">
	// Browse the run's checkout as it is right now. Read-only: the agent owns
	// the files while it works; editing is what "Open editor" is for.
	import { api } from '$lib/api';
	import type { WorkspaceEntry, WorkspaceFile } from '$lib/domain';
	import { onMount } from 'svelte';

	let { runId, repo = '' }: { runId: string; repo?: string } = $props();

	let dirs = $state<Record<string, WorkspaceEntry[]>>({});
	let open = $state<Record<string, boolean>>({ '': true });
	let error = $state('');
	let selected = $state('');
	let file = $state<WorkspaceFile | null>(null);
	let fileError = $state('');
	let showIgnored = $state(false);

	async function load(path: string) {
		try {
			dirs = { ...dirs, [path]: await api.workspace.tree(runId, path, repo) };
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
			file = await api.workspace.file(runId, path, repo);
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
	<div class="msg">Files are unavailable: {error}</div>
{:else}
	<div class="split">
		<div class="tree">
			<div class="nodes">
				{#if !dirs['']}<div class="msg"><span class="d pulse"></span>Loading…</div>{/if}
				{#each rows as r (r.entry.path)}
					<button
						class="node"
						class:sel={r.entry.path === selected}
						class:ignored={r.entry.ignored}
						class:dir={r.entry.dir}
						style:padding-left="{8 + r.depth * 12}px"
						onclick={() => (r.entry.dir ? toggle(r.entry) : view(r.entry.path))}
						title={r.entry.path}
					>
						{#if r.entry.dir}{open[r.entry.path] ? '▾' : '▸'}&nbsp;{/if}{r.entry.name}{r.entry.dir
							? '/'
							: ''}
					</button>
				{/each}
			</div>
			<div class="tf">
				<label class="ig"><input type="checkbox" bind:checked={showIgnored} /> Show ignored</label>
				<button class="btn-icon" title="Refresh" onclick={reload}>↻</button>
			</div>
		</div>
		<div class="viewer">
			{#if !selected}
				<div class="msg">Pick a file.</div>
			{:else}
				<div class="vh">
					{selected}
					{#if file}<span class="fnt"
							>· {fmtSize(file.size)}{file.truncated ? ' · first 1 MB shown' : ''}</span
						>{/if}
				</div>
				{#if fileError}
					<div class="msg">{fileError}</div>
				{:else if !file}
					<div class="msg"><span class="d pulse"></span>Loading…</div>
				{:else if file.binary}
					<div class="bin">Binary file · {fmtSize(file.size)}</div>
				{:else}
					<div class="code">
						{#each lines as l, i (i)}
							<div class="cl"><span class="ln">{i + 1}</span><span class="tx2">{l}</span></div>
						{/each}
					</div>
				{/if}
			{/if}
		</div>
	</div>
{/if}

<style>
	.msg {
		color: var(--mut);
		font-size: 12px;
		padding: 10px 12px;
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
	.split {
		flex: 1;
		min-height: 0;
		display: grid;
		grid-template-columns: 200px minmax(0, 1fr);
		gap: 8px;
		padding: 8px;
	}
	.tree {
		border-radius: 13px;
		background: var(--p2);
		padding: 6px;
		display: flex;
		flex-direction: column;
		min-height: 0;
	}
	.nodes {
		flex: 1;
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
	}
	.node {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		padding: 2px 8px;
		border-radius: 6px;
		font-family: var(--mono);
		font-size: 11.5px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		flex: none;
	}
	.node:hover {
		background: var(--p1);
	}
	.node.sel {
		background: var(--brandBg);
		color: var(--brand);
	}
	.node.ignored {
		color: var(--fnt);
	}
	.tf {
		flex: none;
		margin-top: 4px;
		padding: 4px 4px 0 8px;
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 11.5px;
		color: var(--fnt);
	}
	.ig {
		margin: 0;
		display: flex;
		gap: 6px;
		align-items: center;
		font-size: 11.5px;
		color: var(--fnt);
		font-weight: 400;
		cursor: pointer;
	}
	.viewer {
		min-width: 0;
		min-height: 0;
		display: flex;
		flex-direction: column;
		border-radius: 13px;
		background: var(--code);
		overflow: hidden;
	}
	.vh {
		flex: none;
		padding: 7px 12px;
		font-family: var(--mono);
		font-size: 11.5px;
		font-weight: 500;
		border-bottom: 1px solid var(--ln);
		overflow-wrap: anywhere;
	}
	.vh .fnt {
		font-weight: 400;
	}
	.bin {
		margin: auto;
		color: var(--mut);
	}
	.code {
		flex: 1;
		min-height: 0;
		overflow: auto;
		padding: 8px 0;
		font-family: var(--mono);
		font-size: 11px;
		line-height: 1.65;
	}
	.cl {
		display: grid;
		grid-template-columns: 44px 1fr;
		min-width: max-content;
	}
	.ln {
		text-align: right;
		padding-right: 10px;
		color: var(--fnt);
		user-select: none;
	}
	.tx2 {
		white-space: pre;
	}
	@container (max-width: 540px) {
		.split {
			grid-template-columns: minmax(0, 1fr);
			grid-template-rows: minmax(90px, 35%) minmax(0, 1fr);
		}
	}
</style>
