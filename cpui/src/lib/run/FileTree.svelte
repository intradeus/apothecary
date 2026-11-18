<script lang="ts">
	// The Files pane's tree for one root: a checkout, or an absolute folder
	// (Go to path) labelled with its path and a way back to the checkout.
	// Folders list when first opened; what git ignores is hidden until asked.
	// The state (root, listings, expanded folders) lives in the run's store.
	import type { WorkspaceEntry } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import { rootKey, type OpenFiles } from './openFiles.svelte';

	let { files }: { files: OpenFiles } = $props();

	const isAbs = $derived('abs' in files.root);
	const listed = $derived(files.dirs[files.rootPath]);
	/** The failed folder of this root, if any. */
	const error = $derived(
		files.folderError?.root === rootKey(files.root) ? files.folderError : null
	);
	/** The tab shown, when it belongs to this tree's root. */
	const selected = $derived.by(() => {
		const loc = files.active?.loc;
		if (!loc) return '';
		const root = files.root;
		if ('abs' in loc) return 'abs' in root ? loc.abs : '';
		return 'repo' in root && root.repo === loc.repo ? loc.path : '';
	});

	type Row = { entry: WorkspaceEntry; depth: number };
	/** The visible tree, flattened in display order. */
	const rows = $derived.by(() => {
		const out: Row[] = [];
		const walk = (path: string, depth: number) => {
			for (const e of files.dirs[path] ?? []) {
				if (e.ignored && !files.showIgnored) continue;
				out.push({ entry: e, depth });
				if (e.dir && files.isOpen(e.path)) walk(e.path, depth + 1);
			}
		};
		walk(files.rootPath, 0);
		return out;
	});

	function pick(e: WorkspaceEntry) {
		const root = files.root;
		if (e.dir) files.toggle(e.path);
		else files.open('abs' in root ? { abs: e.path } : { repo: root.repo, path: e.path });
	}
</script>

{#if error && !listed && error.path === files.rootPath}
	<div class="tree">
		<ErrorCard
			cause={t('run.files.unavailable')}
			detail={error.message}
			onretry={() => files.retryFolder()}
		/>
	</div>
{:else}
	<div class="tree">
		{#if isAbs}
			<div class="root">
				<span class="mono truncate" title={files.rootPath}>{files.rootPath}</span>
				<button class="btn btn-ghost btn-sm" onclick={() => files.toCheckout()}
					>{t('run.files.backToCheckout')}</button
				>
			</div>
		{/if}
		<div class="nodes">
			{#if !listed}<Loading />{/if}
			{#each rows as r (r.entry.path)}
				<button
					class="rowbtn node mono"
					class:on={r.entry.path === selected}
					class:ignored={r.entry.ignored}
					style:padding-left="{8 + r.depth * 12}px"
					aria-expanded={r.entry.dir ? files.isOpen(r.entry.path) : undefined}
					onclick={() => pick(r.entry)}
					title={r.entry.path}
				>
					<span class="truncate"
						>{#if r.entry.dir}{files.isOpen(r.entry.path) ? I.menu : I.caret}&nbsp;{/if}{r.entry
							.name}{r.entry.dir ? '/' : ''}</span
					>
				</button>
			{/each}
		</div>
		{#if error}
			<div class="ferr">
				<ErrorCard
					cause={t('run.files.folderError', { path: error.path || '.' })}
					detail={error.message}
					onretry={() => files.retryFolder()}
					ondismiss={() => (files.folderError = null)}
				/>
			</div>
		{/if}
		<div class="tf">
			<Checkbox size="sm" bind:checked={files.showIgnored} label={t('run.files.showIgnored')} />
			<button
				class="btn-icon"
				title={t('run.files.refresh')}
				aria-label={t('run.files.refresh')}
				onclick={() => files.refresh()}>{I.retry}</button
			>
		</div>
	</div>
{/if}

<style>
	.tree {
		border-radius: var(--r-card);
		background: var(--fill-2);
		padding: 6px;
		display: flex;
		flex-direction: column;
		min-height: 0;
		min-width: 0;
	}
	.root {
		flex: none;
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 2px 4px 6px 8px;
		border-bottom: 1px solid var(--line);
		margin-bottom: 4px;
	}
	.root .mono {
		flex: 1;
		min-width: 0;
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
</style>
