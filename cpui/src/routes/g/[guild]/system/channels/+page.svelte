<script lang="ts">
	// Channels (DESIGN 7.10): Matrix and Slack as one-way mirrors of the
	// inbox. Pings are always answered in Apothecary, never in the channel.
	//
	// The engine can list, add and remove channels; it has no test or edit
	// endpoint, so a change is remove + add.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { ChannelEntry } from '$lib/domain';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import ConfigCard, { type ConfigRow } from '$lib/ui/ConfigCard.svelte';
	import AddCard from '$lib/system/AddCard.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { esc } from '$lib/ui/html';
	import { I } from '$lib/ui/icons';
	import type { Hue } from '$lib/ui/hues';
	import { materialize, ms, spring } from '$lib/ui/motion';
	import { flip } from 'svelte/animate';

	let entries = $state<ChannelEntry[]>([]);
	let loaded = $state(false);
	let loadError = $state('');
	let adding = $state(false);
	let kind = $state<'matrix' | 'slack'>('matrix');
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
			loadError = e instanceof Error ? e.message : String(e);
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
				await api.system.addChannel('slack', { webhook_url: webhookUrl.trim() });
				webhookUrl = '';
			}
			adding = false;
			toast(`${label(kind)} channel added`);
			await refresh();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}

	async function remove(c: ChannelEntry) {
		const ok = await confirmDialog({
			title: `Remove this ${label(c.kind)} channel?`,
			body: `Pings stop being mirrored to <b>${esc(where(c))}</b>. Nothing else changes: pings are still raised and answered in Apothecary, and no run is affected.`,
			action: 'Remove channel'
		});
		if (!ok) return;
		error = '';
		removing = c.id;
		try {
			await api.system.removeChannel(c.id);
			toast(`${label(c.kind)} channel removed`);
			await refresh();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			removing = null;
		}
	}

	const label = (k: string) => (k === 'matrix' ? 'Matrix' : k === 'slack' ? 'Slack' : k);
	const hue = (k: string): Hue => (k === 'matrix' ? 'tea' : k === 'slack' ? 'vio' : 'mut');

	/** Where pings land, without leaking the webhook's secret path. */
	function where(c: ChannelEntry): string {
		if (c.kind === 'matrix') return `${c.config.room_id ?? 'room'}`;
		if (c.kind === 'slack') {
			try {
				return `${new URL(String(c.config.webhook_url ?? '')).host} webhook`;
			} catch {
				return 'webhook';
			}
		}
		return c.kind;
	}
	function detail(c: ChannelEntry): ConfigRow[] {
		if (c.kind === 'matrix')
			return [
				{ k: 'Homeserver', v: String(c.config.homeserver ?? '') },
				{ k: 'Room', v: String(c.config.room_id ?? '') },
				{ k: 'Access token', v: '•••• set', secret: true }
			];
		if (c.kind === 'slack') return [{ k: 'Webhook', v: `•••• ${where(c)}`, secret: true }];
		return Object.entries(c.config).map(([k, v]) => ({ k, v: String(v) }));
	}
</script>

<Callout hue="tea" glyph={I.forward}>
	<b>One-way mirrors.</b> New pings are posted to the channel as they are raised. Pings are always answered
	in Apothecary.
</Callout>

{#if loadError && !loaded}
	<ErrorCard cause="Couldn’t load channels" detail={loadError} onretry={refresh} />
{:else if !loaded}
	<div class="card-grid">
		<Skeleton pane lines={3} />
		<Skeleton pane lines={3} />
	</div>
{:else}
	<SectionHeader title="Channels" count={entries.length}>
		{#snippet actions()}
			<button class="btn-sm" class:btn-sel={adding} onclick={() => (adding = !adding)}
				>{adding ? 'Close' : `${I.add} Add channel`}</button
			>
		{/snippet}
	</SectionHeader>

	{#if error && !adding}<ErrorCard cause="That didn’t work" detail={error} />{/if}

	{#if adding}
		<AddCard title="New channel">
			{#snippet head()}
				<Segmented
					glass={false}
					size="sm"
					label="Channel kind"
					options={[
						{ value: 'matrix', label: 'Matrix' },
						{ value: 'slack', label: 'Slack' }
					]}
					bind:value={kind}
				/>
			{/snippet}
			<form class="col" onsubmit={add}>
				{#if kind === 'matrix'}
					<div class="grid2">
						<div class="field">
							<label for="c-hs">Homeserver</label>
							<input id="c-hs" bind:value={homeserver} required placeholder="https://matrix.org" />
						</div>
						<div class="field">
							<label for="c-room">Room ID</label>
							<input
								id="c-room"
								class="mono"
								bind:value={roomId}
								required
								placeholder="!room:matrix.org"
							/>
							<div class="help">Invite the bot account to this room first.</div>
						</div>
						<div class="field">
							<label for="c-token">Access token</label>
							<input
								id="c-token"
								type="password"
								bind:value={accessToken}
								required
								placeholder="syt_…"
								autocomplete="off"
							/>
							<div class="help">Write-only: stored encrypted, never shown again.</div>
						</div>
					</div>
				{:else}
					<div class="field">
						<label for="c-webhook">Incoming webhook URL</label>
						<input
							id="c-webhook"
							type="password"
							bind:value={webhookUrl}
							required
							placeholder="https://hooks.slack.com/services/T…/B…/…"
							autocomplete="off"
						/>
						<div class="help">
							From a Slack app’s Incoming Webhooks page. Its path is the secret.
						</div>
					</div>
				{/if}
				{#if error}<div class="err">{error}</div>{/if}
				<div class="form-actions">
					<button type="button" class="btn-ghost" onclick={() => (adding = false)}>Cancel</button>
					<button class="btn-primary" type="submit" disabled={busy} aria-busy={busy}
						>{busy ? 'Adding…' : `Add ${label(kind)} channel`}</button
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
						sub="mirrors pings to {where(c)}"
						summary={detail(c)}
					>
						{#snippet status()}<span class="pill sm t-tea">one-way</span>{/snippet}
						{#snippet actions()}
							<span class="fnt tiny">To change it, remove it and add it again.</span>
							<span class="spacer"></span>
							<button
								class="btn-danger btn-sm"
								aria-busy={removing === c.id}
								onclick={() => remove(c)}>Remove…</button
							>
						{/snippet}
					</ConfigCard>
				</div>
			{/each}
		</div>
	{:else if !adding}
		<EmptyState
			title="No channels yet"
			text="Pings show up in the inbox either way. Add Matrix or Slack to hear about them there too."
		>
			<button class="btn-primary" onclick={() => (adding = true)}>{I.add} Add channel</button>
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
