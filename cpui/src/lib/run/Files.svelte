<script lang="ts">
	// Browse the run's workspace as it is right now (DESIGN §7.1): the tree,
	// the open files as tabs (kept per run, across checkouts and absolute
	// paths) and one viewer. Read-only: the agent owns the files while it
	// works; editing is what "Open editor" is for. A selection can be sent
	// as an annotation into the chat that takes one.
	import { onMount } from 'svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { fmtBytes } from '$lib/core/format';
	import CodeViewer from '$lib/ui/CodeViewer.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import AnnotateBox from './AnnotateBox.svelte';
	import FileTabs from './FileTabs.svelte';
	import FileTree from './FileTree.svelte';
	import GoToPath from './GoToPath.svelte';
	import type { ComposerKind } from './composerTarget.svelte';
	import type { RunStore } from './store.svelte';

	let {
		rs,
		visible,
		onannotated
	}: {
		rs: RunStore;
		/** The pane is on screen: live refresh runs only then. */
		visible: boolean;
		/** An annotation landed in that chat's composer. */
		onannotated?: (kind: ComposerKind) => void;
	} = $props();

	const files = $derived(rs.files);
	const checkouts = $derived(rs.run?.checkouts ?? []);
	const active = $derived(files.active);
	const file = $derived(active?.file ?? null);
	const shownPath = $derived(
		active ? ('abs' in active.loc ? active.loc.abs : active.loc.path) : ''
	);

	let goto = $state(false);
	let viewer = $state<CodeViewer>();
	/** The selected lines, and the tab they are in. */
	let selection = $state<{ key: string; from: number; to: number } | null>(null);
	const picked = $derived(selection && selection.key === active?.key ? selection : null);
	const quote = $derived(
		picked && file
			? file.content
					.split('\n')
					.slice(picked.from - 1, picked.to)
					.join('\n')
			: ''
	);

	onMount(() => files.show());
	$effect(() => {
		const f = files;
		f.visible = visible;
		return () => (f.visible = false);
	});

	function close(key: string) {
		files.close(key);
		viewer?.forget(key);
	}

	function sent(kind: ComposerKind) {
		viewer?.clearSelection();
		selection = null;
		onannotated?.(kind);
	}
</script>

<div class="files">
	<div class="bar">
		<button class="btn btn-ghost btn-sm" onclick={() => (goto = true)}>{t('run.files.goTo')}</button
		>
	</div>
	<div class="split">
		<FileTree {files} />
		<div class="viewer">
			{#if files.tabs.length}
				<FileTabs
					tabs={files.tabs}
					active={files.activeKey}
					{checkouts}
					onactivate={(key) => files.activate(key)}
					onclose={close}
					onmove={(from, to) => files.move(from, to)}
				/>
			{/if}
			{#if !active}
				<div class="empty-line">{t('run.files.pick')}</div>
			{:else}
				<div class="vh mono">
					{shownPath}
					{#if file}<span class="faint"
							>· {fmtBytes(file.size)}{file.truncated ? ` · ${t('run.files.firstMb')}` : ''}</span
						>{/if}
				</div>
				{#if active.gone}
					<div class="pad" role="alert">
						<p>{t('run.files.gone')}</p>
						<button class="btn btn-sm" onclick={() => close(active.key)}
							>{t('run.files.closeTab')}</button
						>
					</div>
				{:else if active.error}
					<div class="pad">
						<ErrorCard
							cause={t('run.files.readError')}
							detail={active.error}
							onretry={() => files.reload(active.key)}
						/>
					</div>
				{:else if !file}
					<Loading />
				{:else}
					{#if active.deleted}
						<div class="notice" role="status">{t('run.files.deleted')}</div>
					{/if}
					{#if file.binary}
						<div class="bin">{t('run.files.binary', { size: fmtBytes(file.size) })}</div>
					{:else}
						<div class="code">
							<CodeViewer
								bind:this={viewer}
								content={file.content}
								path={shownPath}
								label={t('run.files.viewer', { path: shownPath })}
								docKey={active.key}
								line={active.line}
								onrevealed={() => files.revealed(active.key)}
								onselect={(r) => (selection = r && { key: active.key, ...r })}
							/>
						</div>
						{#if picked}
							<div class="abox">
								<AnnotateBox
									registry={rs.composers}
									loc={active.loc}
									from={picked.from}
									to={picked.to}
									{quote}
									{checkouts}
									onsent={sent}
									oncancel={() => {
										viewer?.clearSelection();
										selection = null;
									}}
								/>
							</div>
						{/if}
						{#if file.truncated}
							<div class="diff-foot">
								{t('ui.code.truncated', { detail: t('run.files.firstMb') })}
							</div>
						{/if}
					{/if}
				{/if}
			{/if}
		</div>
	</div>
</div>

<GoToPath
	runId={rs.runId}
	open={goto}
	onclose={() => (goto = false)}
	onfile={(abs) => files.open({ abs })}
	ondir={(path) => files.setRoot({ abs: path })}
/>

<style>
	.files {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	.bar {
		flex: none;
		display: flex;
		justify-content: flex-end;
		padding: 6px 8px 0;
	}
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
	.notice {
		flex: none;
		padding: 6px 12px;
		color: var(--warning-text);
		font-size: var(--fs-meta);
		border-bottom: 1px solid var(--line);
	}
	.code {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	.bin {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 28px 16px;
		color: var(--text-muted);
		font-size: var(--fs-label);
	}
	.abox {
		flex: none;
		border-top: 1px solid var(--line);
		padding: 8px;
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
