<script lang="ts">
	// The WORKSPACE's controls: the Env menu's and the rebuild menu's triggers,
	// and the editor. The menus themselves are the header's: they open beside
	// these buttons, from outside the glass.
	import { api } from '$lib/api';
	import { errorMessage } from '$lib/core/errors';
	import { openRunEditor } from '$lib/app/editor';
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
	const canRebuild = $derived(rs.canRebuild);
	// The engine's word as well as ours: a build outlives this page, so a
	// reload or a second tab must not offer a rebuild on top of it.
	const rebuilding = $derived(actions.has('rebuild') || run.envStatus === 'rebuilding');

	const asleep = $derived(run.envStatus === 'asleep' || run.envStatus === 'waking');
	const waking = $derived(actions.has('wake') || run.envStatus === 'waking');

	function wake() {
		return actions.run('wake', async () => {
			rs.error = '';
			try {
				await api.runs.wake(run.id);
				return true;
			} catch (e) {
				rs.error = errorMessage(e);
				return false;
			}
		});
	}

	function openEditor() {
		return actions.run('editor', async () => {
			rs.error = '';
			try {
				await openRunEditor(run.id);
				return true;
			} catch (e) {
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
	{#if asleep}
		<button
			disabled={!rs.canShell || waking}
			aria-busy={waking}
			onclick={wake}
			title={rs.canShell ? t('run.header.wakeTitle') : t('common.needsPermission')}
			>{t('run.header.wake')}</button
		>
	{/if}
	<button
		bind:this={envBtn}
		disabled={!rs.hasWorkspace}
		title={rs.hasWorkspace ? t('run.env.links') : t('run.env.noLinks')}
		aria-expanded={envOpen}
		onclick={() => (envOpen = !envOpen)}>{t('run.env.button')} {I.menu}</button
	>
	<button
		disabled={!rs.hasWorkspace || !rs.canShell || actions.has('editor')}
		aria-busy={actions.has('editor')}
		onclick={openEditor}
		title={rs.canShell ? t('run.header.editorTitle') : t('common.needsPermission')}
		>{t('run.header.editor')} {I.external}</button
	>
	<button
		bind:this={rebuildBtn}
		disabled={!canRebuild || !rs.canOperate || rebuilding}
		aria-busy={rebuilding}
		aria-expanded={rebuildOpen}
		title={!rs.canOperate
			? t('common.needsPermission')
			: run.envDirty
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
