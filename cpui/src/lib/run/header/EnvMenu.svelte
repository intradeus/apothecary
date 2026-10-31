<script lang="ts">
	// The Env menu: into the workspace from a desktop (the SSH door), then what
	// the workspace serves.
	import { api } from '$lib/api';
	import { to } from '$lib/core/urls';
	import { t } from '$lib/i18n/index.svelte';
	import type { RunSsh } from '$lib/domain';
	import { I } from '$lib/design/icons';
	import { toast } from '$lib/ui/toast.svelte';
	import Popover from '$lib/ui/Popover.svelte';
	import { vscodeRemoteSettings } from '../derive';
	import type { RunStore } from '../store.svelte';

	let {
		rs,
		anchor,
		open = $bindable(false)
	}: { rs: RunStore; anchor: HTMLElement | null; open: boolean } = $props();

	const run = $derived(rs.run!);
	let copied = $state('');

	// Hold the live ports and services feeds while the menu is open (its app
	// links, a healthy service's preview links).
	$effect(() => {
		if (!open) return;
		const ports = rs.usePorts();
		const services = rs.useServices();
		return () => (ports(), services());
	});

	// The SSH door's facts for this run (TODO 46), read each time the menu
	// opens: a key registered in another tab counts at the next open. The last
	// answer stays on screen while a fresh one is on its way.
	let ssh = $state<RunSsh | null>(null);
	let sshState = $state<'idle' | 'loading' | 'ready' | 'failed'>('idle');
	let sshFor = '';
	$effect(() => {
		if (!open) return;
		const id = run.id;
		if (sshFor !== id) {
			sshFor = id;
			ssh = null;
		}
		let stale = false;
		sshState = 'loading';
		api.runs.ssh(id).then(
			(s) => {
				if (stale) return;
				ssh = s;
				sshState = 'ready';
			},
			() => {
				if (!stale) sshState = 'failed';
			}
		);
		return () => {
			stale = true;
		};
	});
	// The line that pins the door's host key in ~/.ssh/known_hosts.
	const knownHosts = $derived(
		ssh?.enabled && ssh.host && ssh.hostKey
			? `${ssh.port && ssh.port !== 22 ? `[${ssh.host}]:${ssh.port}` : ssh.host} ${ssh.hostKey}`
			: ''
	);

	async function copy(
		l: { kind: string; command?: string },
		said = t('run.env.copied', { kind: l.kind })
	) {
		if (!l.command) return;
		try {
			await navigator.clipboard.writeText(l.command);
			toast(said);
			copied = l.kind;
			setTimeout(() => copied === l.kind && (copied = ''), 1500);
		} catch {
			rs.error = t('run.env.copyFailed');
		}
	}
</script>

<Popover
	{anchor}
	bind:open
	placement="below-end"
	width={300}
	title={t('run.env.title')}
	legend={rs.links.length ? t('run.env.legend', { count: rs.links.length }) : undefined}
>
	<!-- Into the workspace from a desktop (TODO 46): the VS Code link, the
	     ssh command, the door's host key — or what stands in the way. -->
	{#if ssh === null}
		<div class="menu-item still" aria-busy={sshState === 'loading'}>
			<span class="sub"
				>{sshState === 'failed' ? t('run.env.sshFailed') : t('run.env.sshLoading')}</span
			>
		</div>
	{:else if !ssh.enabled}
		<div class="menu-item still">
			<span class="col wrap"
				><span>{t('run.env.sshOff')}</span><span class="sub">{ssh.reason}</span></span
			>
		</div>
	{:else if ssh.keys === 0}
		<a class="menu-item" href={to.account()}>
			<span class="col"
				><span>{t('run.env.sshNoKey')}</span><span class="sub">{t('run.env.sshNoKeySub')}</span
				></span
			>
			<span class="g">{I.forward}</span>
		</a>
	{:else}
		{@const s = ssh}
		{#if s.vscodeUrl}
			{@const settingsLines = vscodeRemoteSettings(s.extensions)}
			<a class="menu-item" href={s.vscodeUrl} title={s.vscodeUrl}>
				<span class="col"
					><span>{t('run.env.vscode')}</span><span class="sub truncate"
						>{s.path ?? t('run.env.vscodeSub')}</span
					></span
				>
				<span class="g">{I.external}</span>
			</a>
			<!-- The desktop's extensions follow it in: two settings.json lines,
			     the list pre-filled with what the workspace's editor runs. -->
			<button
				class="menu-item"
				title={t('run.env.copyTitle', { command: settingsLines })}
				onclick={() =>
					copy({ kind: 'settings', command: settingsLines }, t('run.env.copiedVscodeSettings'))}
			>
				<span class="col"
					><span>{t('run.env.vscodeExts')}</span><span class="sub mono truncate"
						>{t('run.env.vscodeExtsSub')}</span
					></span
				>
				<span class="cp" class:hue-success={copied === 'settings'}
					>{copied === 'settings' ? `${t('run.env.copiedLabel')} ${I.ok}` : t('run.env.copy')}</span
				>
			</button>
		{/if}
		{#if s.command}
			<button
				class="menu-item"
				title={t('run.env.copyTitle', { command: s.command })}
				onclick={() => copy({ kind: 'SSH', command: s.command })}
			>
				<span class="col"
					><span>{t('run.env.sshCommand')}</span><span class="sub mono truncate">{s.command}</span
					></span
				>
				<span class="cp" class:hue-success={copied === 'SSH'}
					>{copied === 'SSH' ? `${t('run.env.copiedLabel')} ${I.ok}` : t('run.env.copy')}</span
				>
			</button>
		{/if}
		{#if knownHosts}
			<button
				class="menu-item"
				title={t('run.env.copyTitle', { command: knownHosts })}
				onclick={() =>
					copy({ kind: 'known_hosts', command: knownHosts }, t('run.env.copiedKnownHosts'))}
			>
				<span class="col"
					><span>{t('run.env.sshKnownHosts')}</span><span class="sub mono truncate"
						>{knownHosts}</span
					></span
				>
				<span class="cp" class:hue-success={copied === 'known_hosts'}
					>{copied === 'known_hosts'
						? `${t('run.env.copiedLabel')} ${I.ok}`
						: t('run.env.copy')}</span
				>
			</button>
		{/if}
		{#if s.fingerprint}
			<div class="menu-item still">
				<span class="sub mono truncate" title={s.fingerprint}
					>{t('run.env.sshFingerprint', { fingerprint: s.fingerprint })}</span
				>
			</div>
		{/if}
	{/if}
	<!-- What the workspace serves: its app ports and service previews. -->
	{#if rs.links.length}
		<div class="menu-div"></div>
		{#each rs.links as l (l.kind + l.url)}
			<a class="menu-item" href={l.url} target="_blank" rel="noreferrer" title={l.url}>
				<span class="col"><span>{l.kind}</span><span class="sub truncate">{l.url}</span></span>
				<span class="g">{I.external}</span>
			</a>
		{/each}
	{/if}
</Popover>

<style>
	.cp {
		font-size: var(--fs-help);
		font-weight: 600;
		color: var(--text-muted);
	}
	.cp.hue-success {
		color: var(--success-text);
	}
	/* A row that only says something: no hover, nothing to press. */
	.still {
		cursor: default;
	}
	.still:hover {
		background: none;
	}
	/* Menu rows don't wrap; these explain themselves in a sentence. */
	.wrap {
		white-space: normal;
	}
	.wrap .sub {
		line-height: 1.35;
	}
</style>
