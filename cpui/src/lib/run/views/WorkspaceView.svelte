<script lang="ts">
	// Changes, Files and Terminal (DESIGN §6): views into the live workspace,
	// each with its own repo picker on a multi-repo run. Without a workspace
	// they say so and point at the Record, which outlives it. Files keeps its
	// tabs and tree in the run's store: its picker only moves the tree's root.
	import { t } from '$lib/i18n/index.svelte';
	import Changes from '../Changes.svelte';
	import Files from '../Files.svelte';
	import Terminal from '../Terminal.svelte';
	import RepoBar from '../RepoBar.svelte';
	import NoWorkspace from '../NoWorkspace.svelte';
	import type { RunStore } from '../store.svelte';
	import type { ComposerKind } from '../composerTarget.svelte';

	let {
		rs,
		kind,
		visible,
		onrecord,
		onannotated
	}: {
		rs: RunStore;
		kind: 'changes' | 'files' | 'terminal';
		visible: boolean;
		onrecord: () => void;
		/** An annotation from Files landed in that chat's composer. */
		onannotated?: (kind: ComposerKind) => void;
	} = $props();

	let repo = $state('');
	/** The tree's checkout ('' is the primary); while it shows an absolute
	 *  folder, a value no checkout has, so the picker marks none. */
	const treeRepo = $derived('repo' in rs.files.root ? rs.files.root.repo : '\0');
	const checkouts = $derived(rs.run?.checkouts ?? []);
	const dir = $derived(
		(repo ? checkouts.find((c) => c.name === repo) : checkouts.find((c) => c.primary))?.dir ?? ''
	);
	const hint = $derived(
		kind === 'changes'
			? rs.terminal
				? ''
				: t('runview.workspace.hint_changes')
			: kind === 'files'
				? t('runview.workspace.hint_files')
				: t('runview.workspace.hint_terminal')
	);
</script>

{#if !rs.hasWorkspace}
	<NoWorkspace {rs} {onrecord} />
{:else}
	<div class="wv">
		{#if kind === 'files'}
			{#if checkouts.length > 1}
				<RepoBar
					{checkouts}
					bind:value={() => treeRepo, (v) => rs.files.setRoot({ repo: v })}
					{hint}
				/>
			{/if}
			<Files {rs} {visible} {onannotated} />
		{:else}
			{#if checkouts.length > 1}
				<RepoBar {checkouts} bind:value={repo} {hint} />
			{/if}
			{#key repo}
				{#if kind === 'changes'}
					<Changes
						runId={rs.runId}
						{repo}
						active={!rs.terminal}
						{visible}
						oncount={(n) => (rs.changesCount = repo ? rs.changesCount : n)}
					/>
				{:else}
					<Terminal runId={rs.runId} {repo} {dir} />
				{/if}
			{/key}
		{/if}
	</div>
{/if}

<style>
	.wv {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	.wv :global(> :not(:first-child)) {
		min-height: 0;
	}
</style>
