<script lang="ts">
	import { api } from '$lib/api';
	import type { Settings } from '$lib/domain';
	import { onMount } from 'svelte';

	let settings = $state<Settings | null>(null);
	let error = $state('');
	let saved = $state(false);
	let busy = $state(false);

	onMount(async () => {
		try {
			settings = await api.system.settings();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	});

	async function save(e: SubmitEvent) {
		e.preventDefault();
		if (!settings) return;
		busy = true;
		error = '';
		saved = false;
		try {
			settings = await api.system.saveSettings({
				...settings,
				maxConcurrentRuns: Number(settings.maxConcurrentRuns),
				maxEnvironments: Number(settings.maxEnvironments)
			});
			saved = true;
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}
</script>

<h1>Settings</h1>
<p class="muted small" style="margin-top:-8px">
	Policy knobs, applied on the next admission decision — no restart needed. Connections live under <a
		href="/integrations">Integrations</a
	>; notification mirrors under
	<a href="/channels">Channels</a>.
</p>

{#if settings}
	<form class="card" onsubmit={save} style="max-width:520px">
		<h2>Limits</h2>
		<label for="s-conc"
			>Max concurrent runs <span class="muted">— beyond this, tickets queue as pending (FIFO)</span
			></label
		>
		<input id="s-conc" type="number" min="1" bind:value={settings.maxConcurrentRuns} />
		<label for="s-envs"
			>Max environments <span class="muted"
				>— at the cap, new tickets are refused until runs are archived</span
			></label
		>
		<input id="s-envs" type="number" min="1" bind:value={settings.maxEnvironments} />

		<h2 style="margin-top:18px">Reaper</h2>
		<label for="s-after">Destroy completed environments after</label>
		<input
			id="s-after"
			bind:value={settings.destroyCompletedAfter}
			class="mono"
			placeholder="48h"
		/>
		<label for="s-abandoned"
			>Destroy abandoned <span class="muted"
				>— abandoned runs are inspection material; only "never" is accepted</span
			></label
		>
		<input id="s-abandoned" bind:value={settings.destroyAbandoned} class="mono" />

		<h2 style="margin-top:18px">Storage &amp; retention</h2>
		<label for="s-orphans"
			>Sweep unclaimed uploads after <span class="muted"
				>— files uploaded but never attached to a ticket, run or answer</span
			></label
		>
		<input id="s-orphans" bind:value={settings.orphanUploadsAfter} class="mono" placeholder="24h" />
		<label for="s-snaps"
			>Expire snapshot content after <span class="muted"
				>— "never", or a duration; the record (name, hash, size) is kept either way, only the bytes
				go</span
			></label
		>
		<input id="s-snaps" bind:value={settings.snapshotsAfter} class="mono" placeholder="never" />

		{#if error}<div class="error">{error}</div>{/if}
		{#if saved}<div class="small" style="color:var(--green); margin-top:8px">Saved.</div>{/if}
		<div style="margin-top:14px">
			<button class="primary" type="submit" disabled={busy}>Save settings</button>
		</div>
	</form>
{:else if error}
	<div class="error">{error}</div>
{:else}
	<div class="empty">Loading…</div>
{/if}
