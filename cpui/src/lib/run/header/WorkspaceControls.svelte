<script lang="ts">
	// The WORKSPACE's controls: the Env menu's and the rebuild menu's triggers,
	// and the editor. The menus themselves are the header's: they open beside
	// these buttons, from outside the glass.
	import { errorMessage } from '$lib/core/errors';
	import { api } from '$lib/api';
	import { t } from '$lib/i18n/index.svelte';
	import { I } from '$lib/design/icons';
	import GlassGroup from '$lib/ui/GlassGroup.svelte';
	import WorkspaceStatus from '$lib/ui/WorkspaceStatus.svelte';
	import type { RunStore } from '../store.svelte';
	import type { HeaderActions } from './actions.svelte';

	let {
		rs,
		actions,
		envBtn = $bindable(null),
		rebuildBtn = $bindable(null),
		envOpen = $bindable(false),
		rebuildOpen = $bindable(false)
	}: {
		rs: RunStore;
		actions: HeaderActions;
		envBtn: HTMLButtonElement | null;
		rebuildBtn: HTMLButtonElement | null;
		envOpen: boolean;
		rebuildOpen: boolean;
	} = $props();

	const run = $derived(rs.run!);
	const canRebuild = $derived(
		!!run.envId && run.state !== 'archived' && (rs.terminal || run.state === 'paused')
	);
	// The engine's word as well as ours: a build outlives this page, so a
	// reload or a second tab must not offer a rebuild on top of it.
	const rebuilding = $derived(actions.has('rebuild') || run.envStatus === 'rebuilding');

	function openEditor() {
		return actions.run('editor', async () => {
			rs.error = '';
			// Open the tab inside the click so it is not blocked; code-server boots
			// for a few seconds on first open.
			const w = window.open('', '_blank');
			try {
				const url = await api.runs.openEditor(run.id);
				if (w) w.location.href = url;
				else window.open(url, '_blank');
				return true;
			} catch (e) {
				w?.close();
				rs.error = errorMessage(e);
				return false;
			}
		});
	}
</script>

<GlassGroup>
	{#snippet label()}
		<WorkspaceStatus state={run.state} envId={run.envId} envStatus={run.envStatus} />
	{/snippet}
	<button
		bind:this={envBtn}
		disabled={!rs.hasWorkspace}
		title={rs.hasWorkspace ? t('run.env.links') : t('run.env.noLinks')}
		aria-expanded={envOpen}
		onclick={() => (envOpen = !envOpen)}>{t('run.env.button')} {I.menu}</button
	>
	<button
		disabled={!rs.hasWorkspace || actions.has('editor')}
		aria-busy={actions.has('editor')}
		onclick={openEditor}
		title={t('run.header.editorTitle')}>{t('run.header.editor')} {I.external}</button
	>
	<button
		bind:this={rebuildBtn}
		disabled={!canRebuild || rebuilding}
		aria-busy={rebuilding}
		aria-expanded={rebuildOpen}
		title={run.envDirty
			? t(canRebuild ? 'run.rebuild.dirtyTitle' : 'run.rebuild.dirtyTitleBusy')
			: canRebuild
				? t('run.rebuild.title')
				: run.envId
					? t('run.rebuild.pauseFirst')
					: t('run.rebuild.noWorkspace')}
		onclick={() => (rebuildOpen = !rebuildOpen)}
		>{#if run.envDirty}<span class="dirty" aria-hidden="true"></span>{/if}{t('run.rebuild.button')}
		{I.menu}</button
	>
</GlassGroup>

<style>
	/* The devcontainer config moved on since the build: the rebuild is due. */
	.dirty {
		display: inline-block;
		width: 7px;
		height: 7px;
		margin-right: 6px;
		border-radius: 50%;
		background: var(--warning);
		box-shadow: 0 0 6px var(--warning);
		vertical-align: middle;
	}
</style>
