<script lang="ts">
	// Egress (DESIGN §6): every host this workspace reached or was refused,
	// and the rule in force NOW per host — toggled live, saved to the
	// workshop's policy. "Seen" is history and never changes; "rule now" is
	// what the gateway does today.
	import { api } from '$lib/api';
	import { to } from '$lib/guild';
	import { fleet } from '$lib/fleet.svelte';
	import { ago, fmtAbs, fmtBytes } from '$lib/format';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';
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

	// The rule flips the moment it is clicked (optimistic); a refusal puts
	// it back and says why.
	let flipped = $state<Record<string, 'allow' | 'deny'>>({});
	const shownRule = (h: string) => flipped[h] ?? ruleFor(h);

	async function setRule(h: string, allow: boolean, undo = false) {
		const prev = ruleFor(h);
		toggling = { ...toggling, [h]: true };
		flipped = { ...flipped, [h]: allow ? 'allow' : 'deny' };
		error = '';
		try {
			await api.egress.setRule(rs.runId, h, allow);
			policy = await api.egress.policy(rs.runId);
			await rs.refreshDenials();
			toast(`${h} ${allow ? 'allowed' : 'blocked'}${undo ? ' again' : ''}`, {
				tone: allow ? 'ok' : 'warn',
				action:
					prev && !undo
						? { label: 'Undo', run: () => void setRule(h, prev === 'allow', true) }
						: undefined
			});
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			const next = { ...flipped };
			delete next[h];
			flipped = next;
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

<div class="view">
	<div class="vbar">
		<Segmented
			glass={false}
			size="sm"
			label="Verdict"
			bind:value={rs.egressVerdict}
			options={[
				{ value: '', label: 'All', count: all.length },
				{ value: 'allow', label: 'Allowed', count: nAllow },
				{ value: 'deny', label: 'Denied', count: nDeny }
			]}
		/>
		<input class="q" placeholder="Filter host…" bind:value={host} aria-label="Filter host" />
	</div>
	{#if error}
		<div class="pad">
			<ErrorCard
				cause={rows === null ? 'The egress audit is unavailable.' : 'The rule did not change.'}
				detail={error}
				onretry={rows === null ? refresh : undefined}
				ondismiss={() => (error = '')}
			/>
		</div>
	{/if}
	{#if rows === null && !error}
		<Loading text="Connecting…" />
	{:else if rows === null}
		<!-- the error card above says why -->
	{:else if all.length === 0}
		<div class="empty-line">
			No egress recorded yet. Every host this workspace reaches — or is refused — shows up here.
		</div>
	{:else}
		<div class="rows card-in">
			<div class="r head">
				<span>Host:port</span><span>Seen</span><span>Rule now</span><span>Proto</span><span
					class="rt">×</span
				><span class="rt">Out/in</span><span class="rt">Last</span>
			</div>
			{#each shown as r (r.host + ':' + r.port + ':' + r.verdict)}
				{@const rule = shownRule(r.host)}
				<div class="r">
					<span class="mono hostc ell" title={r.dstIp ? `${r.host} (${r.dstIp})` : r.host}
						>{r.host}{r.port && r.port !== 443 ? `:${r.port}` : ''}</span
					>
					<span
						class="seen {r.verdict === 'allow' ? 'hue-grn' : 'hue-ros'}"
						title="What happened to this traffic when it occurred — history, never changed by rule edits"
						>{r.verdict === 'allow' ? 'allowed' : 'blocked'}</span
					>
					<span>
						{#if rule}
							<span
								class="tg"
								class:busy={toggling[r.host]}
								title="Allow or block this host now (live, saved to the workshop)"
							>
								<Segmented
									glass={false}
									size="sm"
									label="Rule for {r.host}"
									value={rule}
									onchange={(v) => v !== ruleFor(r.host) && setRule(r.host, v === 'allow')}
									options={[
										{ value: 'allow', label: 'allow', tone: 'grn' },
										{ value: 'deny', label: 'block', tone: 'ros' }
									]}
								/>
							</span>
						{:else}
							<span class="fnt" title="No live gateway — the workspace is gone">{I.none}</span>
						{/if}
					</span>
					<span class="fnt sm">{r.proto}</span>
					<span class="rt mono">{r.count}</span>
					<span class="rt mono fnt">{fmtBytes(r.bytesOut) || '0'}/{fmtBytes(r.bytesIn) || '0'}</span
					>
					<span class="rt mono fnt" title={fmtAbs(r.lastSeen)}>{ago(r.lastSeen)}</span>
				</div>
			{/each}
			{#if shown.length === 0}<div class="empty-line">Nothing matches.</div>{/if}
		</div>
	{/if}
	<div class="foot">
		Rule changes apply live and are saved to
		{#if rs.run?.projectId}<a class="brand" href={to.workshopSettings(rs.run.projectId)}
				>{project?.name ?? `the workshop`} › workspace policy</a
			>{:else}the workshop's policy{/if}{policy
			? ` (${policy.mode === 'deny' ? 'denylist' : 'allowlist'})`
			: ''}.
	</div>
</div>

<style>
	.q {
		margin-left: auto;
		flex: 1 1 100px;
		max-width: 180px;
		padding: 3px 10px;
		font-size: var(--fs-label);
	}
	.pad {
		padding: 0 8px 8px;
	}
	.rows {
		margin: 0 8px;
		overflow: hidden;
		font-size: var(--fs-label);
		flex: none;
	}
	.r {
		grid-template-columns: minmax(0, 1fr) 58px 112px 40px 34px 92px 64px;
		gap: 0 6px;
		padding: 4px 12px;
	}
	.r.head {
		padding-top: 8px;
		padding-bottom: 8px;
	}
	.hostc {
		font-size: var(--fs-meta);
	}
	.seen {
		font-weight: 600;
		font-size: var(--fs-meta);
	}
	.tg {
		display: inline-flex;
	}
	.tg.busy {
		pointer-events: none;
		opacity: var(--o-dim);
	}
	.sm {
		font-size: var(--fs-meta);
	}
	.rt {
		text-align: right;
	}
	.r .mono {
		font-size: var(--fs-id);
	}
	.r .hostc {
		font-size: var(--fs-meta);
	}
	.foot {
		padding: 10px 12px;
		color: var(--mut);
		font-size: var(--fs-label);
	}
	.foot a {
		font-weight: 500;
	}
	/* Drop columns as the window narrows: container queries on the window
	 * (not the viewport), sized to when the columns stop fitting. */
	@container (max-width: 560px) {
		.r {
			grid-template-columns: minmax(0, 1fr) 54px 108px 34px;
		}
		.r > :nth-child(4),
		.r > :nth-child(6),
		.r > :nth-child(7) {
			display: none;
		}
	}
	@container (max-width: 380px) {
		.r {
			grid-template-columns: minmax(0, 1fr) 50px 100px;
		}
		.r > :nth-child(5) {
			display: none;
		}
	}
</style>
