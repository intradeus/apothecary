<script lang="ts">
	// Record (DESIGN §6): everything this run put on the record — each stage
	// attempt's declared outputs and log (captured on success and failure),
	// and the files you attached. Snapshots outlive the workspace.
	import { api } from '$lib/api';
	import { to } from '$lib/guild';
	import { fmtAbs, fmtBytes, fmtClock } from '$lib/format';
	import Segmented from '$lib/ui/Segmented.svelte';
	import type { Hue } from '$lib/ui/hues';
	import { I } from '$lib/ui/icons';
	import type { RunStore } from '../store.svelte';

	let { rs }: { rs: RunStore } = $props();

	type Kind = 'output' | 'log' | 'attachment';
	interface Row {
		id: string;
		filename: string;
		kind: Kind;
		snapshot: boolean;
		label: string;
		source: string;
		href?: string;
		size: number;
		at: string;
	}

	let filter = $state<'' | Kind>('');

	const all = $derived.by<Row[]>(() => {
		const rows: Row[] = rs.snapshots.map((sn) => ({
			id: sn.id,
			filename: sn.filename,
			kind: sn.kind === 'log' ? 'log' : 'output',
			snapshot: true,
			label:
				sn.kind === 'log' ? 'log' : sn.kind === 'ping_file' ? 'ping file' : (sn.name ?? 'output'),
			source:
				sn.kind === 'ping_file'
					? 'shown in a ping'
					: `${sn.stageId} · attempt ${sn.iteration ?? 0}`,
			href: sn.pingId ? to.ping(sn.pingId) : undefined,
			size: sn.size,
			at: sn.createdAt
		}));
		for (const a of rs.attachments)
			rows.push({
				id: a.id,
				filename: a.filename,
				kind: 'attachment',
				snapshot: false,
				label: 'attachment',
				source: a.pingId ? 'ping answer' : 'run goal',
				href: a.pingId ? to.ping(a.pingId) : undefined,
				size: a.size,
				at: a.createdAt
			});
		return rows.sort((a, b) => (a.at < b.at ? -1 : a.at > b.at ? 1 : 0));
	});
	const rows = $derived(filter ? all.filter((r) => r.kind === filter) : all);
	const n = (k: Kind) => all.filter((r) => r.kind === k).length;
	const hue: Record<Kind, Hue> = { output: 'vio', log: 'tea', attachment: 'brand' };
</script>

<div class="view">
	<div class="vbar">
		<Segmented
			glass={false}
			size="sm"
			label="Kind"
			bind:value={filter}
			options={[
				{ value: '', label: 'All', count: all.length },
				{ value: 'output', label: 'Outputs', count: n('output') },
				{ value: 'log', label: 'Logs', count: n('log') },
				{ value: 'attachment', label: 'Attachments', count: n('attachment') }
			]}
		/>
		<span class="hint">Kept after the workspace is destroyed</span>
	</div>
	{#if all.length === 0}
		<div class="empty-line">
			Nothing on the record yet. Each stage attempt files its declared outputs and its log here as
			it finishes, whether it passed or failed.
		</div>
	{:else if rows.length === 0}
		<div class="empty-line">No files of that kind.</div>
	{:else}
		<div class="rows card-in">
			<div class="r head">
				<span>Name</span><span>Kind</span><span>Source</span><span class="rt">Size</span><span
					class="rt">Time</span
				><span></span>
			</div>
			{#each rows as r (r.id)}
				{@const url = r.snapshot ? api.files.snapshotUrl(r.id) : api.files.attachmentUrl(r.id)}
				{@const dl = r.snapshot
					? api.files.snapshotUrl(r.id, true)
					: api.files.attachmentUrl(r.id, true)}
				<div class="r">
					<a class="nm mono ell tx" href={url} target="_blank" rel="noreferrer" title={r.filename}
						>{r.filename}</a
					>
					<span><span class="pill sm flat sq t-{hue[r.kind]}">{r.label}</span></span>
					<span class="mut ell"
						>{#if r.href}<a class="src" href={r.href}>{r.source}</a>{:else}{r.source}{/if}</span
					>
					<span class="rt mono">{fmtBytes(r.size)}</span>
					<span class="rt mono fnt" title={r.at ? fmtAbs(r.at) : ''}
						>{r.at ? fmtClock(r.at) : I.none}</span
					>
					<span class="acts"
						><a class="brand" href={url} target="_blank" rel="noreferrer">Open</a><a
							class="brand"
							href={dl}
							title="Download {r.filename}"
							aria-label="Download {r.filename}">{I.follow}</a
						></span
					>
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.rows {
		margin: 0 8px 10px;
		overflow: hidden;
		font-size: var(--fs-label);
		flex: none;
	}
	.r {
		grid-template-columns: minmax(0, 1.4fr) 92px minmax(0, 1fr) 62px 48px 64px;
		gap: 0 8px;
		padding: 5px 12px;
	}
	.r.head {
		padding-top: 8px;
		padding-bottom: 8px;
	}
	.r .mono {
		font-size: var(--fs-id);
	}
	.r .nm {
		font-size: var(--fs-meta);
	}
	.src {
		color: inherit;
	}
	.src:hover {
		color: var(--tx);
	}
	.rt {
		text-align: right;
	}
	.acts {
		display: flex;
		gap: 8px;
		justify-content: flex-end;
		font-weight: 500;
	}
	/* Drop columns as the window narrows (a container query on the window). */
	@container (max-width: 560px) {
		.r {
			grid-template-columns: minmax(0, 1fr) 80px 56px 54px;
		}
		.r > :nth-child(3),
		.r > :nth-child(5) {
			display: none;
		}
	}
</style>
