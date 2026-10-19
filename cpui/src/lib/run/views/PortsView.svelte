<script lang="ts">
	// Ports (DESIGN §6): listeners the workspace scanner found (click exposes
	// and opens), what is already exposed, and a manual "expose a port".
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import { CODE_SERVER_PORT } from '../derive';
	import { I } from '$lib/ui/icons';
	import { toast } from '$lib/ui/toast.svelte';
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
			toast(t('runview.ports.toast_exposed', { port }), {
				action: { label: t('runview.ports.toast_open'), run: () => window.open(res.url, '_blank') }
			});
		} catch (e) {
			w?.close();
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = null;
		}
	}
	async function exposeInput() {
		const port = parseInt(input, 10);
		if (!port || port < 1 || port > 65535) {
			error = t('runview.ports.invalid');
			return;
		}
		await expose(port);
		if (!error) input = '';
	}

	const exposed = $derived(rs.exposed.filter((p) => p.port !== CODE_SERVER_PORT));
</script>

<div class="view pt">
	<div class="lbl">{t('runview.ports.detected')}</div>
	{#if rs.observed.length === 0}
		<div class="note-line">{t('runview.ports.nothing_new')}</div>
	{:else}
		<div class="list">
			{#each rs.observed as p (p.port)}
				<button
					class="port"
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
			{/each}
		</div>
	{/if}
	{#if exposed.length}
		<div class="lbl">{t('runview.ports.exposed')}</div>
		<div class="list">
			{#each exposed as p (p.port)}
				<a class="btn port" href={p.url} target="_blank" rel="noreferrer" title={p.url}
					><span class="pn mono">:{p.port}</span> {t('runview.ports.open')} {I.external}</a
				>
			{/each}
		</div>
	{/if}
	{#if !rs.terminal}
		<div class="lbl">{t('runview.ports.expose_a_port')}</div>
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
				class="btn-primary"
				disabled={busy !== null || !input}
				aria-busy={busy !== null && !rs.observed.some((p) => p.port === busy)}
				onclick={exposeInput}>{t('runview.ports.expose_btn')}</button
			>
		</div>
		<div class="help">
			{t('runview.ports.help')}
		</div>
	{/if}
	{#if error}<div class="err">{error}</div>{/if}
</div>

<style>
	.pt {
		padding: 12px;
		gap: 10px;
	}
	.pt > * {
		flex: none;
	}
	.lbl:not(:first-child) {
		margin-top: 6px;
	}
	.note-line {
		font-size: var(--fs-label);
		color: var(--mut);
	}
	.help {
		color: var(--fnt);
	}
	.list {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.port {
		padding: 6px 11px;
	}
	.port:hover {
		border-color: var(--tea);
	}
	.pn {
		color: var(--tea);
	}
	.ex {
		display: flex;
		gap: 6px;
	}
	.ex input {
		width: 130px;
		padding: 5px 11px;
		border-radius: var(--r-btn);
	}
	.ex .btn-primary {
		padding: 5px 14px;
	}
</style>
