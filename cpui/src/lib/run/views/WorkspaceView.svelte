<script lang="ts">
	// Changes, Files and Terminal (DESIGN §6): views into the live workspace,
	// each with its own repo picker on a multi-repo run. Without a workspace
	// they say so and point at the Record, which outlives it.
	import { t } from '$lib/i18n/index.svelte';
	import Changes from '../Changes.svelte';
	import Files from '../Files.svelte';
	import Terminal from '../Terminal.svelte';
	import RepoBar from '../RepoBar.svelte';
	import NoWorkspace from '../NoWorkspace.svelte';
	import type { RunStore } from '../store.svelte';

	let {
		rs,
		kind,
		visible,
		onrecord
	}: {
		rs: RunStore;
		kind: 'changes' | 'files' | 'terminal';
		visible: boolean;
		onrecord: () => void;
	} = $props();

	let repo = $state('');
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
			{:else if kind === 'files'}
				<Files runId={rs.runId} {repo} />
			{:else}
				<Terminal runId={rs.runId} {repo} {dir} />
			{/if}
		{/key}
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
