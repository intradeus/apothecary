<script lang="ts">
	// A shell in the run's workspace, in the repo, as the workspace user —
	// over a WebSocket to the workspace sidecar's PTY. The session lives as
	// long as this view: leaving the tab ends the shell.
	import { api } from '$lib/api';
	import '@xterm/xterm/css/xterm.css';

	let { runId }: { runId: string } = $props();

	let host = $state<HTMLDivElement | null>(null);
	let status = $state<'connecting' | 'open' | 'closed'>('connecting');
	let generation = $state(0); // bump to reconnect

	// One terminal + socket per (host, generation): reconnect bumps generation,
	// the effect's cleanup disposes the old pair and this builds a new one.
	$effect(() => {
		void generation;
		const el = host;
		if (!el) return;
		let disposed = false;
		let cleanup = () => {};
		const start = async () => {
			const [{ Terminal }, { FitAddon }] = await Promise.all([
				import('@xterm/xterm'),
				import('@xterm/addon-fit')
			]);
			if (disposed) return;
			const term = new Terminal({
				cursorBlink: true,
				fontFamily: "ui-monospace, 'JetBrains Mono', monospace",
				fontSize: 13,
				theme: { background: '#0f1115', foreground: '#e6e9f0', cursor: '#8b7cf6' }
			});
			const fit = new FitAddon();
			term.loadAddon(fit);
			el.replaceChildren();
			term.open(el);
			fit.fit();

			let ws: WebSocket | null = null;
			const enc = new TextEncoder();
			const connect = () => {
				status = 'connecting';
				ws = new WebSocket(api.workspace.terminalUrl(runId, term.cols, term.rows));
				ws.binaryType = 'arraybuffer';
				ws.onopen = () => {
					status = 'open';
					term.focus();
				};
				ws.onmessage = (m) => {
					if (m.data instanceof ArrayBuffer) term.write(new Uint8Array(m.data));
					else term.write(String(m.data));
				};
				ws.onclose = () => {
					status = 'closed';
					term.write('\r\n\x1b[90m[session closed]\x1b[0m\r\n');
				};
			};
			const input = term.onData((d) => {
				if (ws?.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
			});
			const resize = term.onResize(({ cols, rows }) => {
				if (ws?.readyState === WebSocket.OPEN)
					ws.send(JSON.stringify({ type: 'resize', cols, rows }));
			});
			const ro = new ResizeObserver(() => fit.fit());
			ro.observe(el);
			connect();
			cleanup = () => {
				ro.disconnect();
				input.dispose();
				resize.dispose();
				ws?.close();
				term.dispose();
			};
		};
		start();
		return () => {
			disposed = true;
			cleanup();
		};
	});
</script>

<div class="bar small muted">
	<span class="dot {status}"></span>
	{status === 'open'
		? 'connected — a shell in the repo'
		: status === 'connecting'
			? 'connecting…'
			: 'disconnected'}
	{#if status === 'closed'}
		<button class="linklike" onclick={() => generation++}>reconnect</button>
	{/if}
</div>
<div class="term" bind:this={host}></div>

<style>
	.bar {
		display: flex;
		gap: 6px;
		align-items: center;
		margin-bottom: 6px;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--muted);
	}
	.dot.open {
		background: var(--green);
	}
	.dot.connecting {
		background: var(--amber);
	}
	.linklike {
		background: none;
		border: none;
		padding: 0;
		color: var(--accent);
		cursor: pointer;
		font: inherit;
	}
	.term {
		height: 62vh;
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 6px;
		background: #0f1115;
	}
</style>
