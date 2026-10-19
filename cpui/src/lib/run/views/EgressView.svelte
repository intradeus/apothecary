<script lang="ts">
	import { loreLc } from '$lib/lore';
	// Egress (DESIGN §6): every host this workspace reached or was refused,
	// and the rule in force NOW per host — toggled live, saved to the
	// workshop's policy. "Seen" is history and never changes; "rule now" is
	// what the gateway does today.
	import { api } from '$lib/api';
	import { to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { ago, fmtAbs, fmtBytes } from '$lib/format';
	import Segmented from '$lib/ui/Segmented.svelte';
	import type { EgressAuditRow, EgressPolicy } from '$lib/domain';
	import type { RunStore } from '../store.svelte';

	let { rs, visible }: { rs: RunStore; visible: boolean } = $props();

	let rows = $state<EgressAuditRow[] | null>(null);
	let policy = $state<EgressPolicy | null>(null);
	let host = $state('');
	let toggling = $state<Record<string, boolean>>({});
	let error = $state('');

	async function refresh() {
		// The audit is history (always there); the policy needs a live gateway.
		api.egress
			.runAudit(rs.runId)
			.then((a) => ((rows = a), (error = '')))
			.catch((e) => rows === null && (error = e instanceof Error ? e.message : String(e)));
		if (rs.hasWorkspace)
			api.egress
				.policy(rs.runId)
				.then((p) => (policy = p))
				.catch(() => {});
	}

	$effect(() => {
		if (!visible) return;
		void refresh();
		if (rs.terminal) return;
		const t = setInterval(() => !document.hidden && refresh(), 5000);
		return () => clearInterval(t);
	});

	// matchesList mirrors the gateway's hostMatches: exact entry, or a
	// "*.suffix" wildcard matched by suffix (never the bare apex itself).
	function matchesList(h: string, list: string[]): boolean {
		const x = h.toLowerCase();
		return list.some((d) => {
			const e = d.toLowerCase();
			return e.startsWith('*.') ? x.endsWith(e.slice(1)) : x === e;
		});
	}
	/** The CURRENT rule, mirroring the gateway: a force-block wins in both
	 *  modes, then mode + list membership decide. null = no live gateway. */
	function ruleFor(h: string): 'allow' | 'deny' | null {
		if (!policy) return null;
		if (matchesList(h, policy.blockedDomains)) return 'deny';
		const m = matchesList(h, policy.domains);
		if (policy.mode === 'deny') return m ? 'deny' : 'allow';
		return m ? 'allow' : 'deny';
	}

	async function setRule(h: string, allow: boolean) {
		toggling = { ...toggling, [h]: true };
		error = '';
		try {
			await api.egress.setRule(rs.runId, h, allow);
			policy = await api.egress.policy(rs.runId);
			await rs.refreshDenials();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			toggling = { ...toggling, [h]: false };
		}
	}

	const all = $derived(rows ?? []);
	const shown = $derived(
		all.filter(
			(r) =>
				(!rs.egressVerdict || r.verdict === rs.egressVerdict) &&
				(!host.trim() || r.host.toLowerCase().includes(host.trim().toLowerCase()))
		)
	);
	const nAllow = $derived(all.filter((r) => r.verdict === 'allow').length);
	const nDeny = $derived(all.filter((r) => r.verdict === 'deny').length);
	const project = $derived(rs.run?.projectId ? fleet.projectById.get(rs.run.projectId) : undefined);
</script>

<div class="eg">
	<div class="bar">
		<Segmented
			glass={false}
			size="sm"
			bind:value={rs.egressVerdict}
			options={[
				{ value: '', label: 'All', count: all.length },
				{ value: 'allow', label: 'Allowed', count: nAllow },
				{ value: 'deny', label: 'Denied', count: nDeny }
			]}
		/>
		<input class="q" placeholder="Filter host…" bind:value={host} aria-label="Filter host" />
	</div>
	{#if error}<div class="err">{error}</div>{/if}
	{#if rows === null && !error}
		<div class="msg"><span class="d pulse"></span>Connecting…</div>
	{:else if rows === null}
		<div class="msg">The egress audit is unavailable.</div>
	{:else if all.length === 0}
		<div class="msg">
			No egress recorded yet. Every host this workspace reaches — or is refused — shows up here.
		</div>
	{:else}
		<div class="tbl card-in">
			<div class="h">
				<span class="c0">Host:port</span><span>Seen</span><span>Rule now</span><span>Proto</span
				><span class="rt">×</span><span class="rt">Out/in</span><span class="rt c9">Last</span>
			</div>
			{#each shown as r (r.host + ':' + r.port + ':' + r.verdict)}
				{@const rule = ruleFor(r.host)}
				<div class="r">
					<span class="c0 hostc" title={r.dstIp ? `${r.host} (${r.dstIp})` : r.host}
						>{r.host}{r.port && r.port !== 443 ? `:${r.port}` : ''}</span
					>
					<span
						class="seen {r.verdict}"
						title="What happened to this traffic when it occurred — history, never changed by rule edits"
						>{r.verdict === 'allow' ? 'allowed' : 'blocked'}</span
					>
					<span>
						{#if rule}
							<span class="tg" class:busy={toggling[r.host]}>
								<button
									class:on={rule === 'allow'}
									class="al"
									disabled={toggling[r.host] || rule === 'allow'}
									title="Allow this host now (live, saved to the {loreLc('workshop')})"
									onclick={() => setRule(r.host, true)}>allow</button
								><button
									class:on={rule === 'deny'}
									class="bl"
									disabled={toggling[r.host] || rule === 'deny'}
									title="Block this host now (live, saved to the {loreLc('workshop')})"
									onclick={() => setRule(r.host, false)}>block</button
								>
							</span>
						{:else}
							<span class="fnt" title="No live gateway — the workspace is gone">—</span>
						{/if}
					</span>
					<span class="fnt sm">{r.proto}</span>
					<span class="rt mono">{r.count}</span>
					<span class="rt mono fnt">{fmtBytes(r.bytesOut) || '0'}/{fmtBytes(r.bytesIn) || '0'}</span
					>
					<span class="rt mono fnt c9" title={fmtAbs(r.lastSeen)}>{ago(r.lastSeen)}</span>
				</div>
			{/each}
			{#if shown.length === 0}<div class="msg">Nothing matches.</div>{/if}
		</div>
	{/if}
	<div class="foot">
		Rule changes apply live and are saved to
		{#if rs.run?.projectId}<a href={to.workshopSettings(rs.run.projectId)}
				>{project?.name ?? `the ${loreLc('workshop')}`} › workspace policy</a
			>{:else}the {loreLc('workshop')}'s policy{/if}{policy
			? ` (${policy.mode === 'deny' ? 'denylist' : 'allowlist'})`
			: ''}.
	</div>
</div>

<style>
	.eg {
		flex: 1;
		min-height: 0;
		overflow: auto;
		display: flex;
		flex-direction: column;
	}
	.bar {
		display: flex;
		gap: 6px;
		padding: 8px 12px;
		align-items: center;
		flex-wrap: wrap;
	}
	.q {
		margin-left: auto;
		flex: 1 1 100px;
		max-width: 180px;
		padding: 3px 10px;
		font-size: 12px;
		border-radius: 10px;
	}
	.err {
		color: var(--ros);
		font-size: 11.5px;
		padding: 0 12px 6px;
	}
	.msg {
		color: var(--mut);
		font-size: 12px;
		padding: 10px 12px;
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.d {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--az);
	}
	.tbl {
		margin: 0 8px;
		border-radius: 13px;
		overflow: hidden;
		font-size: 12px;
		flex: none;
	}
	.h,
	.r {
		display: grid;
		grid-template-columns: minmax(0, 1fr) 58px 112px 40px 34px 92px 64px;
		align-items: center;
		gap: 0 6px;
	}
	.h {
		font-size: 11px;
		color: var(--fnt);
		padding: 8px 12px 8px 0;
	}
	.r {
		border-top: 1px solid var(--ln);
		padding: 4px 12px 4px 0;
	}
	.r:hover {
		background: var(--p1);
	}
	.c0 {
		padding-left: 12px;
	}
	.hostc {
		font-family: var(--mono);
		font-size: 11px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.seen {
		font-weight: 600;
		font-size: 11px;
	}
	.seen.allow {
		color: var(--grn);
	}
	.seen.deny {
		color: var(--ros);
	}
	.tg {
		display: inline-flex;
		padding: 2px;
		border-radius: 8px;
		background: var(--p1);
	}
	.tg.busy {
		opacity: 0.6;
	}
	.tg button {
		padding: 0 8px;
		border-radius: 12px;
		font-size: 11px;
		font-weight: 600;
		background: transparent;
		border: 0;
		box-shadow: none;
		color: var(--fnt);
	}
	.tg button:disabled {
		opacity: 1;
	}
	.tg button:hover {
		color: var(--tx);
	}
	.tg .al.on {
		background: var(--grnBg);
		color: var(--grn);
	}
	.tg .bl.on {
		background: var(--rosBg);
		color: var(--ros);
	}
	.sm {
		font-size: 11px;
	}
	.rt {
		text-align: right;
	}
	.mono {
		font-size: 10.5px;
	}
	.foot {
		padding: 10px 12px;
		color: var(--mut);
		font-size: 12px;
	}
	.foot a {
		color: var(--brand);
		font-weight: 500;
	}
	@container (max-width: 560px) {
		.h,
		.r {
			grid-template-columns: minmax(0, 1fr) 54px 108px 34px;
		}
		.h > :nth-child(4),
		.r > :nth-child(4),
		.h > :nth-child(6),
		.r > :nth-child(6),
		.h > :nth-child(7),
		.r > :nth-child(7) {
			display: none;
		}
	}
	@container (max-width: 380px) {
		.h,
		.r {
			grid-template-columns: minmax(0, 1fr) 50px 100px;
		}
		.h > :nth-child(5),
		.r > :nth-child(5) {
			display: none;
		}
	}
</style>
