<script lang="ts">
	// Channels (DESIGN 7.10): Matrix, Slack and Discord as one-way mirrors of the
	// inbox. Pings are always answered in Apothecary, never in the channel.
	//
	// The engine can list, add and remove channels; it has no test or edit
	// endpoint, so a change is remove + add.
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { ChannelEntry } from '$lib/domain';
	import { t, th } from '$lib/i18n/index.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import HeaderAction from '$lib/ui/HeaderAction.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import ConfigCard, { type ConfigRow } from '$lib/ui/ConfigCard.svelte';
	import AddCard from '$lib/system/AddCard.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/design/icons';
	import type { Hue } from '$lib/design/hues';
	import { materialize, ms, spring } from '$lib/ui/motion';
	import { flip } from 'svelte/animate';

	let entries = $state<ChannelEntry[]>([]);
	let loaded = $state(false);
	let loadError = $state('');
	let adding = $state(false);
	let kind = $state<'matrix' | 'slack' | 'discord'>('matrix');
	let homeserver = $state('https://matrix.org');
	let accessToken = $state('');
	let roomId = $state('');
	let webhookUrl = $state('');
	let error = $state('');
	let busy = $state(false);
	/** The channel being removed (aria-busy on its button). */
	let removing = $state<string | null>(null);
	const move = () => ({ duration: ms('--dur-spring', 450), easing: spring });

	const refresh = async () => {
		try {
			entries = await api.system.channels();
			loaded = true;
			loadError = '';
		} catch (e) {
			loadError = errorMessage(e);
		}
	};
	onMount(refresh);

	async function add(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			if (kind === 'matrix') {
				await api.system.addChannel('matrix', {
					homeserver: homeserver.trim(),
					access_token: accessToken.trim(),
					room_id: roomId.trim()
				});
				accessToken = roomId = '';
			} else {
				await api.system.addChannel(kind, { webhook_url: webhookUrl.trim() });
				webhookUrl = '';
			}
			adding = false;
			toast(t('page.channels.added', { kind: label(kind) }));
			await refresh();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function remove(c: ChannelEntry) {
		const ok = await confirmDialog({
			title: t('page.channels.removeTitle', { kind: label(c.kind) }),
			body: th('page.channels.removeBody', { where: where(c) }),
			action: t('page.channels.removeAction')
		});
		if (!ok) return;
		error = '';
		removing = c.id;
		try {
			await api.system.removeChannel(c.id);
			toast(t('page.channels.removed', { kind: label(c.kind) }));
			await refresh();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			removing = null;
		}
	}

	const labels: Record<string, string> = { matrix: 'Matrix', slack: 'Slack', discord: 'Discord' };
	const hues: Record<string, Hue> = { matrix: 'accent-2', slack: 'accent', discord: 'attention' };
	const label = (k: string) => labels[k] ?? k;
	const hue = (k: string): Hue => hues[k] ?? 'muted';
	/** Slack and Discord are both a webhook whose URL is the secret. */
	const isWebhook = (k: string) => k === 'slack' || k === 'discord';

	/** Where pings land, without leaking the webhook's secret path. */
	function where(c: ChannelEntry): string {
		if (c.kind === 'matrix') return `${c.config.room_id ?? t('page.channels.room').toLowerCase()}`;
		if (isWebhook(c.kind)) {
			try {
				return t('page.channels.hostWebhook', {
					host: new URL(String(c.config.webhook_url ?? '')).host
				});
			} catch {
				return t('page.channels.webhook').toLowerCase();
			}
		}
		return c.kind;
	}
	function detail(c: ChannelEntry): ConfigRow[] {
		if (c.kind === 'matrix')
			return [
				{ k: t('page.channels.homeserver'), v: String(c.config.homeserver ?? '') },
				{ k: t('page.channels.room'), v: String(c.config.room_id ?? '') },
				{ k: t('page.channels.accessToken'), v: t('page.channels.tokenSet'), secret: true }
			];
		if (isWebhook(c.kind))
			return [{ k: t('page.channels.webhook'), v: `•••• ${where(c)}`, secret: true }];
		return Object.entries(c.config).map(([k, v]) => ({ k, v: String(v) }));
	}
</script>

<Callout hue="accent-2" glyph={I.forward}>
	{@html th('page.channels.callout')}
</Callout>

{#if loadError && !loaded}
	<ErrorCard cause={t('page.channels.loadError')} detail={loadError} onretry={refresh} />
{:else if !loaded}
	<div class="card-grid">
		<Skeleton pane lines={3} />
		<Skeleton pane lines={3} />
	</div>
{:else}
	<HeaderAction>
		<button class="btn-primary btn-lg" aria-pressed={adding} onclick={() => (adding = !adding)}
			>{adding ? t('page.channels.close') : `${I.add} ${t('page.channels.add')}`}</button
		>
	</HeaderAction>
	<SectionHeader title={t('page.system.channels.title')} count={entries.length} />

	{#if error && !adding}<ErrorCard cause={t('page.channels.failed')} detail={error} />{/if}

	{#if adding}
		<AddCard title={t('page.channels.new')}>
			{#snippet head()}
				<Segmented
					glass={false}
					size="sm"
					label={t('page.channels.kind')}
					options={[
						{ value: 'matrix', label: 'Matrix' },
						{ value: 'slack', label: 'Slack' },
						{ value: 'discord', label: 'Discord' }
					]}
					bind:value={kind}
				/>
			{/snippet}
			<form class="col" onsubmit={add}>
				{#if kind === 'matrix'}
					<div class="grid2">
						<div class="field">
							<label for="c-hs">{t('page.channels.homeserver')}</label>
							<input id="c-hs" bind:value={homeserver} required placeholder="https://matrix.org" />
						</div>
						<div class="field">
							<label for="c-room">{t('page.channels.roomId')}</label>
							<input
								id="c-room"
								class="mono"
								bind:value={roomId}
								required
								placeholder="!room:matrix.org"
							/>
							<div class="help">{t('page.channels.roomHelp')}</div>
						</div>
						<div class="field">
							<label for="c-token">{t('page.channels.accessToken')}</label>
							<input
								id="c-token"
								type="password"
								bind:value={accessToken}
								required
								placeholder="syt_…"
								autocomplete="off"
							/>
							<div class="help">{t('page.channels.tokenHelp')}</div>
						</div>
					</div>
				{:else}
					<div class="field">
						<label for="c-webhook">{t('page.channels.webhookUrl')}</label>
						<input
							id="c-webhook"
							type="password"
							bind:value={webhookUrl}
							required
							placeholder={kind === 'discord'
								? 'https://discord.com/api/webhooks/…/…'
								: 'https://hooks.slack.com/services/T…/B…/…'}
							autocomplete="off"
						/>
						<div class="help">
							{t(kind === 'discord' ? 'page.channels.discordHelp' : 'page.channels.webhookHelp')}
						</div>
					</div>
				{/if}
				{#if error}<div class="error">{error}</div>{/if}
				<div class="form-actions">
					<button type="button" class="btn-ghost" onclick={() => (adding = false)}
						>{t('common.cancel')}</button
					>
					<button class="btn-primary" type="submit" disabled={busy} aria-busy={busy}
						>{busy
							? t('page.channels.adding')
							: t('page.channels.addKind', { kind: label(kind) })}</button
					>
				</div>
			</form>
		</AddCard>
	{/if}

	{#if entries.length}
		<div class="card-grid">
			{#each entries as c (c.id)}
				<div class="cell" animate:flip={move()} in:materialize={{ y: 8 }}>
					<ConfigCard
						title={label(c.kind)}
						glyph={I.channels}
						hue={hue(c.kind)}
						sub={t('page.channels.mirrorsTo', { where: where(c) })}
						summary={detail(c)}
					>
						{#snippet status()}<span class="pill sm tint-accent-2">{t('page.channels.oneWay')}</span
							>{/snippet}
						{#snippet actions()}
							<span class="faint tiny">{t('page.channels.changeHint')}</span>
							<span class="spacer"></span>
							<button
								class="btn-danger btn-sm"
								aria-busy={removing === c.id}
								onclick={() => remove(c)}>{t('page.channels.remove')}</button
							>
						{/snippet}
					</ConfigCard>
				</div>
			{/each}
		</div>
	{:else if !adding}
		<EmptyState title={t('page.channels.empty')} text={t('page.channels.emptyText')}>
			<button class="btn-primary" onclick={() => (adding = true)}
				>{I.add} {t('page.channels.add')}</button
			>
		</EmptyState>
	{/if}
{/if}

<style>
	.card-grid {
		--min: 380px;
	}
	.cell {
		min-width: 0;
	}
</style>
