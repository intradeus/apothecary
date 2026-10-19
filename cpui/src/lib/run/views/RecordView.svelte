<script lang="ts">
	// Record (DESIGN §6): everything this run put on the record — each stage
	// attempt's declared outputs and log (captured on success and failure),
	// and the files you attached. Snapshots outlive the workspace.
	import { api } from '$lib/api';
	import { to } from '$lib/guild';
	import { fmtAbs, fmtBytes, fmtClock } from '$lib/format';
	import Segmented from '$lib/ui/Segmented.svelte';
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
	const hue: Record<Kind, string> = { output: 'vio', log: 'tea', attachment: 'brand' };
</script>

<div class="rec">
	<div class="bar">
		<Segmented
			glass={false}
			size="sm"
			bind:value={filter}
			options={[
				{ value: '', label: 'All', count: all.length },
				{ value: 'output', label: 'Outputs', count: n('output') },
				{ value: 'log', label: 'Logs', count: n('log') },
				{ value: 'attachment', label: 'Attachments', count: n('attachment') }
			]}
		/>
		<span class="note">Kept after the workspace is destroyed</span>
	</div>
	{#if all.length === 0}
		<div class="none">
			Nothing on the record yet. Each stage attempt files its declared outputs and its log here as
			it finishes, whether it passed or failed.
		</div>
	{:else if rows.length === 0}
		<div class="none">No files of that kind.</div>
	{:else}
		<div class="tbl card-in">
			<div class="h">
				<span class="c0">Name</span><span>Kind</span><span>Source</span><span class="rt">Size</span
				><span class="rt">Time</span><span></span>
			</div>
			{#each rows as r (r.id)}
				{@const url = r.snapshot ? api.files.snapshotUrl(r.id) : api.files.attachmentUrl(r.id)}
				{@const dl = r.snapshot
					? api.files.snapshotUrl(r.id, true)
					: api.files.attachmentUrl(r.id, true)}
				<div class="r">
					<a class="c0 nm" href={url} target="_blank" rel="noreferrer" title={r.filename}
						>{r.filename}</a
					>
					<span><span class="kc t-{hue[r.kind]}">{r.label}</span></span>
					<span class="src ell"
						>{#if r.href}<a href={r.href}>{r.source}</a>{:else}{r.source}{/if}</span
					>
					<span class="rt mono">{fmtBytes(r.size)}</span>
					<span class="rt mono fnt" title={r.at ? fmtAbs(r.at) : ''}
						>{r.at ? fmtClock(r.at) : '—'}</span
					>
					<span class="acts"
						><a href={url} target="_blank" rel="noreferrer">Open</a><a
							href={dl}
							title="Download {r.filename}">↓</a
						></span
					>
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.rec {
		flex: 1;
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
		padding: 12px 14px;
		gap: 10px;
	}
	.bar {
		display: flex;
		gap: 10px;
		align-items: center;
		flex-wrap: wrap;
	}
	.note {
		margin-left: auto;
		color: var(--fnt);
		font-size: 11.5px;
	}
	.none {
		color: var(--mut);
		font-size: 12.5px;
		max-width: 60ch;
	}
	.tbl {
		border-radius: 14px;
		overflow: hidden;
		font-size: 12px;
	}
	.h,
	.r {
		display: grid;
		grid-template-columns: minmax(0, 1.4fr) 92px minmax(0, 1fr) 62px 48px 64px;
		align-items: center;
		gap: 0 8px;
	}
	.h {
		font-size: 11px;
		color: var(--fnt);
		padding: 8px 0;
	}
	.r {
		border-top: 1px solid var(--ln);
		padding: 5px 0;
	}
	.r:hover {
		background: var(--p1);
	}
	.c0 {
		padding-left: 12px;
	}
	.nm {
		font-family: var(--mono);
		font-size: 11px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.kc {
		padding: 0 7px;
		border-radius: 12px;
		font-size: 11px;
		white-space: nowrap;
	}
	.t-vio {
		background: var(--vioBg);
		color: var(--vio);
	}
	.t-tea {
		background: var(--teaBg);
		color: var(--tea);
	}
	.t-brand {
		background: var(--brandBg);
		color: var(--brand);
	}
	.src {
		color: var(--mut);
	}
	.src a:hover {
		color: var(--tx);
	}
	.rt {
		text-align: right;
	}
	.mono {
		font-size: 10.5px;
	}
	.acts {
		display: flex;
		gap: 8px;
		justify-content: flex-end;
		padding-right: 12px;
		color: var(--brand);
		font-weight: 500;
	}
	@container (max-width: 560px) {
		.h,
		.r {
			grid-template-columns: minmax(0, 1fr) 80px 56px 54px;
		}
		.h > :nth-child(3),
		.r > :nth-child(3),
		.h > :nth-child(5),
		.r > :nth-child(5) {
			display: none;
		}
	}
</style>
