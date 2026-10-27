<script lang="ts">
	import SearchInput from '$lib/ui/SearchInput.svelte';
	// Egress (DESIGN §6): every host this workspace reached or was refused,
	// and the rule in force NOW per host — toggled live, saved to the
	// workshop's policy. "Seen" is history and never changes; "rule now" is
	// what the gateway does today.
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import { to } from '$lib/urls';
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
		const timer = setInterval(() => !document.hidden && refresh(), 5000);
		return () => clearInterval(timer);
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
			toast(
				t(
					allow
						? undo
							? 'runview.egress.toast_allowed_again'
							: 'runview.egress.toast_allowed'
						: undo
							? 'runview.egress.toast_blocked_again'
							: 'runview.egress.toast_blocked',
					{ host: h }
				),
				{
					tone: allow ? 'ok' : 'warn',
					action:
						prev && !undo
							? {
									label: t('runview.egress.undo'),
									run: () => void setRule(h, prev === 'allow', true)
								}
							: undefined
				}
			);
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
	const mode = $derived(
		policy
			? t('runview.egress.mode_paren', {
					mode: t(policy.mode === 'deny' ? 'runview.egress.denylist' : 'runview.egress.allowlist')
				})
			: ''
	);
	const project = $derived(rs.run?.projectId ? fleet.projectById.get(rs.run.projectId) : undefined);
</script>

<div class="view">
	<div class="vbar">
		<Segmented
			glass={false}
			size="sm"
			label={t('runview.egress.verdict')}
			bind:value={rs.egressVerdict}
			options={[
				{ value: '', label: t('runview.egress.all'), count: all.length },
				{ value: 'allow', label: t('runview.egress.allowed'), count: nAllow },
				{ value: 'deny', label: t('runview.egress.denied'), count: nDeny }
			]}
		/>
		<span class="q"
			><SearchInput
				size="sm"
				width={180}
				bind:value={host}
				placeholder={t('runview.egress.filter_ph')}
				label={t('runview.egress.filter_aria')}
			/></span
		>
	</div>
	{#if error}
		<div class="pad">
			<ErrorCard
				cause={rows === null
					? t('runview.egress.audit_unavailable')
					: t('runview.egress.rule_unchanged')}
				detail={error}
				onretry={rows === null ? refresh : undefined}
				ondismiss={() => (error = '')}
			/>
		</div>
	{/if}
	{#if rows === null && !error}
		<Loading text={t('runview.connecting')} />
	{:else if rows === null}
		<!-- the error card above says why -->
	{:else if all.length === 0}
		<div class="empty-line">
			{t('runview.egress.empty')}
		</div>
	{:else}
		<div class="rows card-in">
			<div class="r head">
				<span>{t('runview.egress.col_host')}</span><span>{t('runview.egress.col_seen')}</span><span
					>{t('runview.egress.col_rule')}</span
				><span>{t('runview.egress.col_proto')}</span><span class="rt">×</span><span class="rt"
					>{t('runview.egress.col_out_in')}</span
				><span class="rt">{t('runview.egress.col_last')}</span>
			</div>
			{#each shown as r (r.host + ':' + r.port + ':' + r.verdict)}
				{@const rule = shownRule(r.host)}
				<div class="r">
					<span class="mono hostc ell" title={r.dstIp ? `${r.host} (${r.dstIp})` : r.host}
						>{r.host}{r.port && r.port !== 443 ? `:${r.port}` : ''}</span
					>
					<span
						class="seen {r.verdict === 'allow' ? 'hue-grn' : 'hue-ros'}"
						title={t('runview.egress.seen_title')}
						>{r.verdict === 'allow'
							? t('runview.egress.seen_allowed')
							: t('runview.egress.seen_blocked')}</span
					>
					<span>
						{#if rule}
							<span
								class="tg"
								class:busy={toggling[r.host]}
								title={t('runview.egress.toggle_title')}
							>
								<Segmented
									glass={false}
									size="sm"
									label={t('runview.egress.rule_for', { host: r.host })}
									value={rule}
									onchange={(v) => v !== ruleFor(r.host) && setRule(r.host, v === 'allow')}
									options={[
										{ value: 'allow', label: t('runview.egress.rule_allow'), tone: 'grn' },
										{ value: 'deny', label: t('runview.egress.rule_block'), tone: 'ros' }
									]}
								/>
							</span>
						{:else}
							<span class="fnt" title={t('runview.egress.no_gateway')}>{I.none}</span>
						{/if}
					</span>
					<span class="fnt sm">{r.proto}</span>
					<span class="rt mono">{r.count}</span>
					<span class="rt mono fnt">{fmtBytes(r.bytesOut) || '0'}/{fmtBytes(r.bytesIn) || '0'}</span
					>
					<span class="rt mono fnt" title={fmtAbs(r.lastSeen)}>{ago(r.lastSeen)}</span>
				</div>
			{/each}
			{#if shown.length === 0}<div class="empty-line">
					{t('runview.egress.nothing_matches')}
				</div>{/if}
		</div>
	{/if}
	<div class="foot">
		{#if rs.run?.projectId}
			{@const parts = t('runview.egress.foot_linked', { link: '\u0000', mode }).split('\u0000')}
			{parts[0]}<a class="brand" href={to.workshopSettings(rs.run.projectId)}
				>{t('runview.egress.foot_link', {
					name: project?.name ?? t('runview.egress.the_workshop')
				})}</a
			>{parts[1]}
		{:else}
			{t('runview.egress.foot_plain', { mode })}
		{/if}
	</div>
</div>

<style>
	.q {
		margin-left: auto;
		display: flex;
		flex: 0 1 180px;
		min-width: 0;
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
