<script lang="ts">
	// Browse the run's checkout as it is right now. Read-only: the agent owns
	// the files while it works; editing is what "Open editor" is for.
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import type { WorkspaceEntry, WorkspaceFile } from '$lib/domain';
	import { onMount } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { fileLines } from '$lib/run/diff';
	import { fmtBytes } from '$lib/core/format';
	import { I } from '$lib/design/icons';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import CodeView from '$lib/ui/CodeView.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';

	let { runId, repo = '' }: { runId: string; repo?: string } = $props();

	let dirs = $state<Record<string, WorkspaceEntry[]>>({});
	let open = $state<Record<string, boolean>>({ '': true });
	let error = $state('');
	/** The folder `error` is about ('' is the root). */
	let errorPath = $state('');
	let selected = $state('');
	let file = $state<WorkspaceFile | null>(null);
	let fileError = $state('');
	let showIgnored = $state(false);

	async function load(path: string) {
		try {
			const listed = await api.workspace.tree(runId, path, repo);
			dirs = { ...dirs, [path]: listed };
			if (path === errorPath) error = '';
		} catch (e) {
			error = errorMessage(e);
			errorPath = path;
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
			const read = await api.workspace.file(runId, path, repo);
			if (path === selected) file = read;
		} catch (e) {
			if (path === selected) fileError = errorMessage(e);
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
	const lines = $derived(file && !file.binary ? fileLines(file.content) : []);
</script>

{#if error && !dirs['']}
	<div class="pad">
		<ErrorCard cause={t('run.files.unavailable')} detail={error} onretry={() => load('')} />
	</div>
{:else}
	<div class="split">
		<div class="tree">
			<div class="nodes">
				{#if !dirs['']}<Loading />{/if}
				{#each rows as r (r.entry.path)}
					<button
						class="rowbtn node mono"
						class:on={r.entry.path === selected}
						class:ignored={r.entry.ignored}
						style:padding-left="{8 + r.depth * 12}px"
						aria-expanded={r.entry.dir ? !!open[r.entry.path] : undefined}
						onclick={() => (r.entry.dir ? toggle(r.entry) : view(r.entry.path))}
						title={r.entry.path}
					>
						<span class="truncate"
							>{#if r.entry.dir}{open[r.entry.path] ? I.menu : I.caret}&nbsp;{/if}{r.entry.name}{r
								.entry.dir
								? '/'
								: ''}</span
						>
					</button>
				{/each}
			</div>
			{#if error}
				<div class="ferr">
					<ErrorCard
						cause={t('run.files.folderError', { path: errorPath || '.' })}
						detail={error}
						onretry={() => load(errorPath)}
						ondismiss={() => (error = '')}
					/>
				</div>
			{/if}
			<div class="tf">
				<Checkbox size="sm" bind:checked={showIgnored} label={t('run.files.showIgnored')} />
				<button
					class="btn-icon"
					title={t('run.files.refresh')}
					aria-label={t('run.files.refresh')}
					onclick={reload}>{I.retry}</button
				>
			</div>
		</div>
		<div class="viewer">
			{#if !selected}
				<div class="empty-line">{t('run.files.pick')}</div>
			{:else}
				<div class="vh mono">
					{selected}
					{#if file}<span class="faint"
							>· {fmtBytes(file.size)}{file.truncated ? ` · ${t('run.files.firstMb')}` : ''}</span
						>{/if}
				</div>
				{#if fileError}
					<div class="pad">
						<ErrorCard
							cause={t('run.files.readError')}
							detail={fileError}
							onretry={() => view(selected)}
						/>
					</div>
				{:else if !file}
					<Loading />
				{:else}
					<div class="code">
						<CodeView
							{lines}
							binary={file.binary
								? t('run.files.binary', { size: fmtBytes(file.size) })
								: undefined}
							truncated={file.truncated ? t('run.files.firstMb') : undefined}
						/>
					</div>
				{/if}
			{/if}
		</div>
	</div>
{/if}

<style>
	.pad {
		padding: 10px;
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
		border-radius: var(--r-card);
		background: var(--fill-2);
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
		flex: none;
		padding-top: 2px;
		padding-bottom: 2px;
		border-radius: var(--r-tag);
	}
	.node.ignored {
		color: var(--text-faint);
	}
	.ferr {
		flex: none;
		margin-top: 4px;
		overflow-wrap: anywhere;
	}
	.tf {
		flex: none;
		margin-top: 4px;
		padding: 4px 4px 0 8px;
		display: flex;
		justify-content: space-between;
		align-items: center;
	}
	.viewer {
		min-width: 0;
		min-height: 0;
		display: flex;
		flex-direction: column;
		border-radius: var(--r-card);
		background: var(--code-bg);
		overflow: hidden;
	}
	.vh {
		flex: none;
		padding: 7px 12px;
		border-bottom: 1px solid var(--line);
		overflow-wrap: anywhere;
	}
	.vh .faint {
		font-weight: 400;
	}
	.code {
		flex: 1;
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
	}
	.code > :global(.bin) {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
	}
	/* Stack the tree over the file when the window gets narrow (a container
	 * query on the window, so its own threshold). */
	@container (max-width: 540px) {
		.split {
			grid-template-columns: minmax(0, 1fr);
			grid-template-rows: minmax(90px, 35%) minmax(0, 1fr);
		}
	}
</style>
