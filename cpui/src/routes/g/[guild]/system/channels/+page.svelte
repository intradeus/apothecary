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
	import { esc } from '$lib/system/html';

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
		try {
			await api.system.removeChannel(c.id);
			await refresh();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		}
	}

	const label = (k: string) => (k === 'matrix' ? 'Matrix' : k === 'slack' ? 'Slack' : k);
	const glyph = (k: string) => (k === 'matrix' ? '[m]' : k === 'slack' ? '#' : '✉');

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
	function detail(c: ChannelEntry): { k: string; v: string; secret?: boolean }[] {
		if (c.kind === 'matrix')
			return [
				{ k: 'Homeserver', v: String(c.config.homeserver ?? '') },
				{ k: 'Room', v: String(c.config.room_id ?? '') },
				{ k: 'Access token', v: 'set', secret: true }
			];
		if (c.kind === 'slack') return [{ k: 'Webhook', v: where(c), secret: true }];
		return Object.entries(c.config).map(([k, v]) => ({ k, v: String(v) }));
	}
</script>

<div class="note lg">
	<span class="arrow">⇢</span>
	<span
		><b>One-way mirrors.</b> New pings are posted to the channel as they are raised. Pings are always
		answered in Apothecary.</span
	>
</div>

{#if loadError && !loaded}
	<ErrorCard cause="Couldn’t load channels" detail={loadError} onretry={refresh} />
{:else if !loaded}
	<div class="cards">
		<div class="c card"><Skeleton lines={3} /></div>
		<div class="c card"><Skeleton lines={3} /></div>
	</div>
{:else}
	<div class="gh">
		<h2>Channels</h2>
		<span class="count">{entries.length}</span>
		<span class="spacer"></span>
		<button class="btn-sm" class:btn-sel={adding} onclick={() => (adding = !adding)}
			>{adding ? 'Close' : '+ Add channel'}</button
		>
	</div>

	{#if error && !adding}<div class="err">{error}</div>{/if}

	{#if adding}
		<form class="c add" onsubmit={add}>
			<div class="ah">
				<span class="serif at">New channel</span>
				<span class="spacer"></span>
				<Segmented
					glass={false}
					size="sm"
					options={[
						{ value: 'matrix', label: 'Matrix' },
						{ value: 'slack', label: 'Slack' }
					]}
					bind:value={kind}
				/>
			</div>
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
					<div class="help">From a Slack app’s Incoming Webhooks page. Its path is the secret.</div>
				</div>
			{/if}
			{#if error}<div class="err">{error}</div>{/if}
			<div class="foot">
				<button type="button" class="btn-ghost" onclick={() => (adding = false)}>Cancel</button>
				<button class="btn-primary" type="submit" disabled={busy}
					>{busy ? 'Adding…' : `Add ${label(kind)} channel`}</button
				>
			</div>
		</form>
	{/if}

	{#if entries.length}
		<div class="cards">
			{#each entries as c (c.id)}
				<article class="c card">
					<div class="top">
						<span class="gl {c.kind}">{glyph(c.kind)}</span>
						<div class="who">
							<span class="nm">{label(c.kind)}</span>
							<span class="mut small ell">mirrors pings to {where(c)}</span>
						</div>
						<span class="pill t-tea">one-way</span>
					</div>
					<dl class="cfg">
						{#each detail(c) as d (d.k)}
							<dt>{d.k}</dt>
							<dd class="mono ell" class:secret={d.secret}>{d.secret ? `•••• ${d.v}` : d.v}</dd>
						{/each}
					</dl>
					<div class="acts">
						<span class="fnt tiny">To change it, remove it and add it again.</span>
						<span class="spacer"></span>
						<button class="btn-danger btn-sm" onclick={() => remove(c)}>Remove…</button>
					</div>
				</article>
			{/each}
		</div>
	{:else if !adding}
		<EmptyState
			title="No channels yet"
			text="Pings show up in the inbox either way. Add Matrix or Slack to hear about them there too."
		>
			<button class="btn-primary" onclick={() => (adding = true)}>+ Add channel</button>
		</EmptyState>
	{/if}
{/if}

<style>
	.note {
		border-radius: 14px;
		padding: 9px 14px;
		display: flex;
		align-items: baseline;
		gap: 10px;
		font-size: 12.5px;
		color: var(--tx);
	}
	.arrow {
		color: var(--tea);
	}
	.gh {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 0 2px;
	}
	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
		gap: 10px;
	}
	.card {
		border-radius: 16px;
		padding: 12px 14px 10px;
		display: flex;
		flex-direction: column;
		gap: 10px;
		min-width: 0;
	}
	.top {
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.gl {
		flex: none;
		width: 30px;
		height: 30px;
		border-radius: 9px;
		display: flex;
		align-items: center;
		justify-content: center;
		font-family: var(--mono);
		font-size: 12px;
		font-weight: 600;
		background: var(--p2);
		border: 1px solid var(--ln);
	}
	.gl.matrix {
		color: var(--tea);
	}
	.gl.slack {
		color: var(--vio);
	}
	.who {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.nm {
		font-weight: 600;
		font-size: 13.5px;
	}
	.cfg {
		margin: 0;
		display: grid;
		grid-template-columns: max-content minmax(0, 1fr);
		gap: 3px 12px;
		font-size: 12px;
		padding: 8px 10px;
		border-radius: 10px;
		background: var(--code);
	}
	dt {
		color: var(--mut);
	}
	dd {
		margin: 0;
		font-size: 11.5px;
	}
	dd.secret {
		color: var(--fnt);
	}
	.acts {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.add {
		border-radius: 16px;
		padding: 14px 16px;
		display: flex;
		flex-direction: column;
		gap: 12px;
		border-color: var(--brandLn);
	}
	.ah {
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.at {
		font-size: 19px;
	}
	.foot {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
	}
	@media (max-width: 720px) {
		.cards {
			grid-template-columns: minmax(0, 1fr);
		}
	}
	/* app.css's input reset outranks its own input.mono; restate it. */
	input.mono {
		font-family: var(--mono) !important;
		font-size: 12px !important;
	}
</style>
