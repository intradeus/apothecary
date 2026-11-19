<script lang="ts">
	// Ports (DESIGN §6): one table of the workspace's ports (PortTable, the
	// Services view's too), a row each —
	// who listens (its service, else its process), the port, whether it has
	// a browser link yet, the `apothecary forward` line that brings it onto
	// the person's own machine (any port, any protocol), and, when the
	// engine hands out browser links (an apps domain), "open" in the
	// browser, which exposes it first. The link is a web proxy, so it only
	// serves web apps; the table says so, a database being the usual
	// mistake. Under it: opening a port the scanner has not found, and the
	// two forwards of the whole workspace (free local ports, or `--same`
	// numbers) — of the ports it listens on when run, once, so offered only
	// while it listens on some (the CLI refuses a workspace listening on
	// none). The CLI takes the workspace's name (the one ssh and the Env
	// menu use) or, before there is a workspace, the run id.
	import { errorMessage } from '$lib/core/errors';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import { CODE_SERVER_PORT } from '../derive';
	import { toast } from '$lib/ui/toast.svelte';
	import CopyCommand from '$lib/ui/CopyCommand.svelte';
	import PortTable, { type PortRow } from '../PortTable.svelte';
	import type { RunStore } from '../store.svelte';

	let { rs, visible }: { rs: RunStore; visible: boolean } = $props();

	// Exposing a port and tunnelling to it: a live run, and run:shell in its
	// workshop. Without it the table only reads.
	const reach = $derived(!rs.terminal && rs.canShell);
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
			await rs.refreshPorts(); // the row now has its link
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

	const exposedRow = (p: { port: number; url: string; service?: string; process?: string }) => ({
		port: p.port,
		name: p.service ?? p.process,
		url: p.url,
		status: t('runview.ports.status_exposed'),
		exposed: true
	});
	/** Every port once, by number: exposed ones with their link, then what
	 *  the scanner found (the engine lists a port in one or the other). The
	 *  editor's port has its own button. */
	const rows = $derived.by(() => {
		const out: PortRow[] = rs.exposed.filter((p) => p.port !== CODE_SERVER_PORT).map(exposedRow);
		for (const p of rs.observed) {
			if (!out.some((r) => r.port === p.port)) {
				out.push({
					port: p.port,
					name: p.service ?? p.process,
					status: t('runview.ports.status_listening'),
					exposed: false
				});
			}
		}
		return out.sort((a, b) => a.port - b.port);
	});
	/** What `apothecary forward` is told: the workspace, else the run. */
	const ws = $derived(rs.run?.envId || rs.runId);
	const forwardOne = (port: number) => `apothecary forward ${ws} ${port}`;
</script>

<div class="view pt">
	<div class="caption">{t('runview.ports.title')}</div>
	{#if rows.length === 0}
		<div class="help">{t('runview.ports.nothing')}</div>
	{:else}
		<PortTable
			{rows}
			nameHeader={t('runview.ports.col_service')}
			forward={reach ? forwardOne : undefined}
			links={rs.portLinks}
			onopen={reach ? expose : undefined}
			{busy}
		/>
		{#if reach}<div class="help">{t('runview.ports.forward_hint')}</div>{/if}
		{#if rs.portLinks}<div class="help">{t('runview.ports.web_only')}</div>{/if}
	{/if}
	{#if reach && rs.portLinks}
		<div class="caption">{t('runview.ports.another')}</div>
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
				aria-busy={busy !== null && !rows.some((r) => r.port === busy)}
				onclick={exposeInput}>{t('runview.ports.open_btn')}</button
			>
		</div>
	{/if}
	{#if error}<div class="error">{error}</div>{/if}
	{#if reach && rows.length > 0}
		<div class="caption">{t('runview.ports.forward_all')}</div>
		<CopyCommand command={`apothecary forward ${ws}`} help={t('runview.ports.forward_help')} />
		<CopyCommand
			command={`apothecary forward ${ws} --same`}
			help={t('runview.ports.forward_same_help')}
		/>
	{/if}
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
	.ex {
		display: flex;
		gap: 6px;
	}
	.ex input {
		width: 130px;
	}
</style>
