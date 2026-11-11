<script lang="ts">
	// Ports (DESIGN §6): the commands that bring the workspace's ports onto
	// the person's own machine (`apothecary forward`, which always works:
	// every port on free local ports, or `--same` port numbers), the
	// listeners the workspace scanner found — each with its own one-port
	// command to copy — and, when the engine hands out browser links (an
	// apps domain), click-to-expose, what is already exposed, and a manual
	// "expose a port". The CLI takes the workspace's name (the one ssh and
	// the Env menu use) or, before there is a workspace, the run id.
	import { errorMessage } from '$lib/core/errors';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import { CODE_SERVER_PORT } from '../derive';
	import { I } from '$lib/design/icons';
	import { toast } from '$lib/ui/toast.svelte';
	import CopyCommand from '$lib/ui/CopyCommand.svelte';
	import CopyButton from '$lib/ui/CopyButton.svelte';
	import type { RunStore } from '../store.svelte';

	let { rs, visible }: { rs: RunStore; visible: boolean } = $props();

	$effect(() => {
		if (!visible) return;
		return rs.usePorts();
	});

	let input = $state('');
	let busy = $state<number | null>(null);
	let error = $state('');

	async function expose(port: number) {
		if (!port || port < 1 || port > 65535) return;
		busy = port;
		error = '';
		// Open the tab now (inside the click) so a popup blocker lets it through.
		const w = window.open('', '_blank');
		try {
			const res = await api.runs.exposePort(rs.runId, port);
			await rs.refreshPorts(); // the now-exposed port drops off the detected list
			if (w) w.location.href = res.url;
			else window.open(res.url, '_blank');
			toast(t('runview.ports.toast_exposed', { port: String(port) }), {
				action: { label: t('runview.ports.toast_open'), run: () => window.open(res.url, '_blank') }
			});
		} catch (e) {
			w?.close();
			error = errorMessage(e);
		} finally {
			busy = null;
		}
	}
	async function exposeInput() {
		const port = /^\d+$/.test(input.trim()) ? Number(input) : 0;
		if (!port || port < 1 || port > 65535) {
			error = t('runview.ports.invalid');
			return;
		}
		await expose(port);
		if (!error) input = '';
	}

	const exposed = $derived(rs.exposed.filter((p) => p.port !== CODE_SERVER_PORT));
	/** What `apothecary forward` is told: the workspace, else the run. */
	const ws = $derived(rs.run?.envId || rs.runId);
	const forwardOne = (port: number) => `apothecary forward ${ws} ${port}`;
</script>

<div class="view pt">
	{#if !rs.terminal}
		<div class="caption">{t('runview.ports.forward')}</div>
		<CopyCommand command={`apothecary forward ${ws}`} help={t('runview.ports.forward_help')} />
		<CopyCommand
			command={`apothecary forward ${ws} --same`}
			help={t('runview.ports.forward_same_help')}
		/>
	{/if}
	<div class="caption">{t('runview.ports.detected')}</div>
	{#if rs.observed.length === 0}
		<div class="help">{t('runview.ports.nothing_new')}</div>
	{:else if !rs.portLinks}
		<div class="chips">
			{#each rs.observed as p (p.port)}
				<span class="pill mono"
					><span class="pn">:{p.port}</span>
					{p.service ?? p.process ?? ''}{#if !rs.terminal}<CopyButton
							command={forwardOne(p.port)}
						/>{/if}</span
				>
			{/each}
		</div>
	{:else}
		<div class="list">
			{#each rs.observed as p (p.port)}
				<span class="one">
					<button
						class="btn-md port"
						disabled={busy !== null || rs.terminal}
						aria-busy={busy === p.port}
						title={p.service
							? t('runview.ports.title_service', { service: p.service })
							: p.process
								? t('runview.ports.title_process', { process: p.process })
								: t('runview.ports.title_plain')}
						onclick={() => expose(p.port)}
					>
						<span class="pn mono">:{p.port}</span>
						{p.service ?? p.process ?? ''}
						{I.external}
					</button>
					{#if !rs.terminal}<CopyButton command={forwardOne(p.port)} />{/if}
				</span>
			{/each}
		</div>
	{/if}
	{#if exposed.length}
		<div class="caption">{t('runview.ports.exposed')}</div>
		<div class="list">
			{#each exposed as p (p.port)}
				<span class="one">
					<a class="btn btn-md port" href={p.url} target="_blank" rel="noreferrer" title={p.url}
						><span class="pn mono">:{p.port}</span> {t('runview.ports.open')} {I.external}</a
					>
					{#if !rs.terminal}<CopyButton command={forwardOne(p.port)} />{/if}
				</span>
			{/each}
		</div>
	{/if}
	{#if !rs.terminal && rs.portLinks}
		<div class="caption">{t('runview.ports.expose_a_port')}</div>
		<div class="ex">
			<input
				class="mono"
				inputmode="numeric"
				placeholder={t('runview.ports.placeholder')}
				bind:value={input}
				onkeydown={(e) => e.key === 'Enter' && exposeInput()}
				aria-label={t('runview.ports.aria')}
			/>
			<button
				class="btn-primary btn-md"
				disabled={busy !== null || !input}
				aria-busy={busy !== null && !rs.observed.some((p) => p.port === busy)}
				onclick={exposeInput}>{t('runview.ports.expose_btn')}</button
			>
		</div>
		<div class="help">
			{t('runview.ports.help')}
		</div>
	{/if}
	{#if error}<div class="error">{error}</div>{/if}
</div>

<style>
	.pt {
		padding: 12px;
		gap: 10px;
	}
	.pt > * {
		flex: none;
	}
	.caption:not(:first-child) {
		margin-top: 6px;
	}
	.list {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.one {
		display: inline-flex;
		align-items: center;
		gap: 2px;
	}
	.chips .pill {
		gap: 4px;
	}
	.port:hover {
		border-color: var(--accent-2);
	}
	.pn {
		color: var(--accent-2-text);
	}
	.ex {
		display: flex;
		gap: 6px;
	}
	.ex input {
		width: 130px;
	}
</style>
