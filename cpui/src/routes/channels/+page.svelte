<script lang="ts">
	import { api } from '$lib/api';
	import type { ChannelEntry } from '$lib/domain';
	import { onMount } from 'svelte';

	let entries = $state<ChannelEntry[]>([]);
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
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
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
			await refresh();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}

	async function remove(id: string) {
		error = '';
		try {
			await api.system.removeChannel(id);
			await refresh();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		}
	}

	function summary(c: ChannelEntry): string {
		if (c.kind === 'matrix') return `${c.config.homeserver} · ${c.config.room_id}`;
		if (c.kind === 'slack') return String(c.config.webhook_url ?? '');
		return JSON.stringify(c.config);
	}
</script>

<h1>Notification channels</h1>
<p class="muted small" style="margin-top:-8px">
	One-way mirrors of the inbox — pings are answered here in the CPUI, never in the channel.
</p>

{#each entries as c (c.id)}
	<div class="card row spread">
		<div>
			<strong>{c.kind}</strong>
			<div class="mono small muted">{summary(c)}</div>
		</div>
		<button class="danger" onclick={() => remove(c.id)}>Remove</button>
	</div>
{:else}
	<div class="empty">No channels configured.</div>
{/each}

<form class="card" onsubmit={add}>
	<h2>Add channel</h2>
	<label for="c-kind">Type</label>
	<select id="c-kind" bind:value={kind}>
		<option value="matrix">Matrix</option>
		<option value="slack">Slack</option>
	</select>

	{#if kind === 'matrix'}
		<label for="c-hs">Homeserver</label>
		<input id="c-hs" bind:value={homeserver} required placeholder="https://matrix.org" />
		<label for="c-token">Access token</label>
		<input id="c-token" type="password" bind:value={accessToken} required placeholder="syt_…" />
		<label for="c-room">Room ID</label>
		<input id="c-room" bind:value={roomId} required placeholder="!room:matrix.org" />
	{:else}
		<label for="c-webhook">Incoming webhook URL</label>
		<input
			id="c-webhook"
			type="password"
			bind:value={webhookUrl}
			required
			placeholder="https://hooks.slack.com/services/T…/B…/…"
		/>
	{/if}

	{#if error}<div class="error">{error}</div>{/if}
	<div style="margin-top:14px">
		<button class="primary" type="submit" disabled={busy}>Add channel</button>
	</div>
</form>
