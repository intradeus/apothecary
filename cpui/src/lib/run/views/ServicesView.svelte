<script lang="ts">
	// Services: the workshop's long-running processes (its app, a database)
	// as they stand in this run's workspace — up or not, who started each
	// (a stage, or a person here) and since when, its probe and how that last
	// went, its ports with their preview links — and Start / Restart / Stop
	// by hand, with each one's output a click away.
	//
	// Live: polled every 3s while on screen (a health flip carries no event),
	// and refetched by the store on every service_* event of the stream.
	import { errorMessage } from '$lib/core/errors';
	import { t, th } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import { to } from '$lib/core/urls';
	import { fleet } from '$lib/app/fleet.svelte';
	import { ago, fmtAbs } from '$lib/core/format';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { I } from '$lib/design/icons';
	import TransportIcon from '$lib/ui/TransportIcon.svelte';
	import type { Hue } from '$lib/design/hues';
	import type { ServiceStatus } from '$lib/domain';
	import type { RunStore } from '../store.svelte';

	let { rs, visible, onstage }: { rs: RunStore; visible: boolean; onstage?: (id: string) => void } =
		$props();

	$effect(() => {
		if (!visible) return;
		return rs.useServices();
	});

	const LOG_TAIL = 200;

	type Act = 'start' | 'restart' | 'stop';
	let busy = $state<Record<string, Act | undefined>>({});
	let error = $state('');

	const data = $derived(rs.services);
	const list = $derived(data?.services ?? []);
	const canAct = $derived(!!data?.workspace);
	const upCount = $derived(list.filter((s) => s.state === 'healthy').length);

	/** Whether the workspace runs it right now (or is bringing it up/down). */
	const running = (s: ServiceStatus) =>
		s.state === 'starting' ||
		s.state === 'healthy' ||
		s.state === 'unhealthy' ||
		s.state === 'stopping';
	const neverStarted = (s: ServiceStatus) => s.state === 'stopped' && !s.startedAt;

	function tone(s: ServiceStatus): Hue {
		switch (s.state) {
			case 'healthy':
				return 'success';
			case 'starting':
			case 'stopping':
				return 'warning';
			case 'unhealthy':
				return 'danger';
			case 'exited':
				return s.exitCode ? 'danger' : 'muted';
			default:
				return 'muted';
		}
	}

	function stateWord(s: ServiceStatus): string {
		switch (s.state) {
			case 'healthy':
				return t('runview.services.state.healthy');
			case 'starting':
				return t('runview.services.state.starting');
			case 'stopping':
				return t('runview.services.state.stopping');
			case 'unhealthy':
				return t('runview.services.state.unhealthy');
			case 'exited':
				return s.exitCode !== undefined
					? t('runview.services.state.exited_code', { code: s.exitCode })
					: t('runview.services.state.exited');
			case 'stopped':
				return neverStarted(s)
					? t('runview.services.state.never')
					: t('runview.services.state.stopped');
			default:
				return t('runview.services.state.unknown');
		}
	}

	async function act(s: ServiceStatus, a: Act) {
		if (a === 'stop' && s.by === 'stage' && s.stageId && !rs.terminal) {
			const ok = await confirmDialog({
				title: t('runview.services.confirm_title', { name: s.name }),
				body: th('runview.services.confirm_body', { stage: s.stageId }),
				action: t('runview.services.stop'),
				tone: 'warn'
			});
			if (!ok) return;
		}
		busy = { ...busy, [s.name]: a };
		error = '';
		try {
			const fn =
				a === 'start'
					? api.runs.startService
					: a === 'restart'
						? api.runs.restartService
						: api.runs.stopService;
			const now = await fn(rs.runId, s.name);
			// What the call answered, until the next read says more.
			if (rs.services)
				rs.services = {
					...rs.services,
					services: rs.services.services.map((x) => (x.name === now.name ? now : x))
				};
			toast(
				t(
					a === 'start'
						? 'runview.services.toast_started'
						: a === 'restart'
							? 'runview.services.toast_restarted'
							: 'runview.services.toast_stopped',
					{ name: s.name }
				),
				{ tone: a === 'stop' ? 'info' : 'ok' }
			);
			void rs.refreshServices();
			if (logs[s.name]) void loadLog(s.name);
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = { ...busy, [s.name]: undefined };
		}
	}

	// --- logs ------------------------------------------------------------------

	interface Log {
		text: string | null;
		note: string;
		loading: boolean;
	}
	let logs = $state<Record<string, Log | undefined>>({});
	const outs: Record<string, HTMLDivElement | undefined> = $state({});

	async function loadLog(name: string, following = false) {
		const cur = logs[name];
		if (!cur) return;
		logs[name] = { ...cur, loading: true };
		try {
			const text = await api.runs.serviceLog(rs.runId, name, LOG_TAIL);
			if (!logs[name]) return;
			const el = outs[name];
			const stick = !el || el.scrollHeight - el.scrollTop - el.clientHeight < 40;
			logs[name] = { text, note: '', loading: false };
			if (stick)
				requestAnimationFrame(() => {
					const o = outs[name];
					if (o) o.scrollTop = o.scrollHeight;
				});
		} catch (e) {
			const last = logs[name];
			if (!last) return;
			logs[name] = {
				// One failed read of a followed log is no reason to blank it.
				text: following ? last.text : null,
				note: t('runview.services.log_fail', {
					message: errorMessage(e)
				}),
				loading: false
			};
		}
	}

	function toggleLog(name: string) {
		if (logs[name]) {
			logs[name] = undefined;
			return;
		}
		logs[name] = { text: null, note: '', loading: false };
		void loadLog(name);
	}

	// An open log of a running service follows it while the window is shown.
	$effect(() => {
		if (!visible) return;
		const timer = setInterval(() => {
			if (document.hidden) return;
			for (const s of list)
				if (logs[s.name] && !logs[s.name]!.loading && running(s)) loadLog(s.name, true);
		}, 3000);
		return () => clearInterval(timer);
	});

	const project = $derived(rs.run?.projectId ? fleet.projectById.get(rs.run.projectId) : undefined);
	const settingsHref = $derived(
		rs.run?.projectId ? `${to.workshopSettings(rs.run.projectId)}#services` : ''
	);
	const settingsLabel = $derived(
		t('runview.services.settings_link', {
			name: project?.name || rs.projectName || t('runview.services.the_workshop')
		})
	);
</script>

{#snippet linked(key: 'runview.services.none_text' | 'runview.services.foot')}
	{@const parts = t(key, { link: '\u0000' }).split('\u0000')}
	{parts[0]}{#if settingsHref}<a class="brand" href={settingsHref}>{settingsLabel}</a
		>{:else}{settingsLabel}{/if}{parts[1]}
{/snippet}

<div class="view">
	{#if data && list.length}
		<div class="vbar">
			<span class="sum">
				<span class="live-dot sm hue-{upCount ? 'success' : 'muted'}" class:flat={!upCount}></span>
				{t('runview.services.up_count', { up: upCount, total: list.length })}
			</span>
			{#if canAct}<span class="hint">{t('runview.services.hint')}</span>{/if}
		</div>
	{/if}

	{#if error}
		<div class="pad">
			<ErrorCard
				cause={t('runview.services.action_fail')}
				detail={error}
				ondismiss={() => (error = '')}
			/>
		</div>
	{/if}
	{#if data?.error && list.length}
		<div class="pad">
			<ErrorCard
				cause={t('runview.services.read_error')}
				detail={data.error}
				onretry={() => rs.refreshServices()}
			/>
		</div>
	{/if}

	{#if !data}
		{#if rs.servicesError}
			<div class="pad">
				<ErrorCard
					cause={t('runview.services.unavailable')}
					detail={rs.servicesError}
					onretry={() => rs.refreshServices()}
				/>
			</div>
		{:else}
			<Loading text={t('runview.connecting')} />
		{/if}
	{:else if list.length === 0}
		<div class="empty">
			<div class="et">{t('runview.services.none_title')}</div>
			<div class="help">{@render linked('runview.services.none_text')}</div>
		</div>
	{:else}
		{#if !data.workspace}
			<div class="note nows">
				{rs.destroyedAt
					? t('runview.services.no_workspace_gone', { ago: ago(rs.destroyedAt, rs.now) })
					: t('runview.services.no_workspace')}
			</div>
		{/if}
		<div class="list">
			{#each list as s (s.name)}
				{@const hue = tone(s)}
				{@const b = busy[s.name]}
				{@const on = running(s)}
				{@const log = logs[s.name]}
				<article class="svc card-in" class:off={!on}>
					<div class="head">
						<span class="nm mono">{s.name}</span>
						<span
							class="pill sm tint-{hue}"
							title={s.healthyAt && s.state === 'healthy'
								? t('runview.services.healthy_since', { at: fmtAbs(s.healthyAt) })
								: undefined}
							><span class="dot" class:pulse={s.state === 'starting' || s.state === 'stopping'}
							></span>{stateWord(s)}</span
						>
						<span class="spacer"></span>
						<div class="acts">
							{#if !on}
								<button
									class="btn-sm"
									disabled={!canAct || !!b}
									aria-busy={b === 'start'}
									title={canAct
										? t('runview.services.start_title')
										: t('runview.services.no_workspace_short')}
									onclick={() => act(s, 'start')}
									><TransportIcon kind="play" /> {t('runview.services.start')}</button
								>
							{:else}
								<button
									class="btn-sm"
									disabled={!canAct || !!b || s.state === 'starting' || s.state === 'stopping'}
									aria-busy={b === 'restart'}
									title={t('runview.services.restart_title')}
									onclick={() => act(s, 'restart')}
									>{I.retry} {t('runview.services.restart')}</button
								>
								<button
									class="btn-sm"
									disabled={!canAct || !!b || s.state === 'stopping'}
									aria-busy={b === 'stop'}
									title={t('runview.services.stop_title')}
									onclick={() => act(s, 'stop')}
									><TransportIcon kind="stop" /> {t('runview.services.stop')}</button
								>
							{/if}
							<button
								class="btn-sm"
								class:btn-sel={!!log}
								aria-pressed={!!log}
								aria-expanded={!!log}
								disabled={!canAct && !log}
								onclick={() => toggleLog(s.name)}
								>{log ? t('runview.services.hide_logs') : t('runview.services.logs')}</button
							>
						</div>
					</div>

					{#if s.description}<div class="desc">{s.description}</div>{/if}

					<dl class="kv facts">
						{#if s.startedAt && s.by}
							<dt>{t('runview.services.k_started')}</dt>
							<dd>
								{#if s.by === 'stage' && s.stageId}
									{@const parts = t(
										s.phase ? 'runview.services.by_stage_phase' : 'runview.services.by_stage',
										{ stage: '\u0000', phase: s.phase ?? '' }
									).split('\u0000')}
									{parts[0]}{#if onstage}<button
											class="linklike mono"
											title={t('runview.services.stage_title')}
											onclick={() => onstage(s.stageId!)}>{s.stageId}</button
										>{:else}<span class="mono">{s.stageId}</span>{/if}{parts[1]}
								{:else}
									{t('runview.services.by_human')}
								{/if}
								<span class="faint" title={fmtAbs(s.startedAt)}>· {ago(s.startedAt, rs.now)}</span>
								{#if on}
									<span class="faint"
										>· {s.by === 'stage'
											? t('runview.services.life_stage')
											: t('runview.services.life_human')}</span
									>
								{/if}
							</dd>
						{/if}
						{#if !on && s.endedAt}
							<dt>{t('runview.services.k_ended')}</dt>
							<dd title={fmtAbs(s.endedAt)}>{ago(s.endedAt, rs.now)}</dd>
						{/if}
						<dt>{t('runview.services.k_probe')}</dt>
						<dd>
							{#if s.probe}
								<span class="mono probe">{s.probe}</span>
								{#if s.lastProbeAt}
									<span
										class={s.lastProbeOk ? 'hue-success' : 'hue-danger'}
										title={fmtAbs(s.lastProbeAt)}
										>· {s.lastProbeOk
											? t('runview.services.probe_ok', { ago: ago(s.lastProbeAt, rs.now) })
											: t('runview.services.probe_fail', {
													ago: ago(s.lastProbeAt, rs.now)
												})}</span
									>
								{:else if on}
									<span class="faint">· {t('runview.services.probe_never')}</span>
								{/if}
							{:else}
								<span class="faint">{t('runview.services.no_probe')}</span>
							{/if}
						</dd>
						{#if s.ports.length}
							<dt>{t('runview.services.k_ports')}</dt>
							<dd class="ports">
								{#each s.ports as p (p.port)}
									<span class="port">
										{#if p.url}
											<a
												class="btn btn-sm"
												href={p.url}
												target="_blank"
												rel="noreferrer"
												title={p.url}
												><span class="pn mono">:{p.port}</span>
												{p.label || t('runview.services.open')}
												{I.external}</a
											>
										{:else}
											<span
												class="pill sm mono tint-muted"
												title={t('runview.services.preview_later')}
												>:{p.port}{p.label ? ` ${p.label}` : ''}</span
											>
										{/if}
										<span class="mono faint addr" title={t('runview.services.address_title')}
											>{p.address}</span
										>
									</span>
								{/each}
							</dd>
						{/if}
					</dl>

					{#if s.lastError}
						<div class="lasterr mono">{s.lastError}</div>
					{/if}

					{#if log}
						<div class="logbar">
							<span class="caption">{t('runview.services.log_tail', { n: LOG_TAIL })}</span>
							<span class="spacer"></span>
							<button
								class="btn-icon"
								title={t('runview.services.log_refresh')}
								aria-label={t('runview.services.log_refresh')}
								aria-busy={log.loading}
								onclick={() => loadLog(s.name)}>{I.retry}</button
							>
						</div>
						<div class="log out" bind:this={outs[s.name]}>
							{#if log.text === null}
								{#if log.note}<div class="empty-line">{log.note}</div>{:else}<Loading
										text={t('runview.connecting')}
									/>{/if}
							{:else if !log.text}
								<div class="empty-line">{t('runview.services.log_empty')}</div>
							{:else}
								<pre>{log.text}</pre>
							{/if}
						</div>
					{/if}
				</article>
			{/each}
		</div>
		<div class="foot">{@render linked('runview.services.foot')}</div>
	{/if}
</div>

<style>
	.pad {
		padding: 0 8px 8px;
	}
	.sum {
		display: inline-flex;
		align-items: center;
		gap: 7px;
		font-size: var(--fs-label);
		color: var(--text-muted);
	}
	.empty {
		padding: 18px 16px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		max-width: 520px;
	}
	.et {
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--fs-section);
	}
	.empty .help,
	.foot {
		color: var(--text-muted);
		font-size: var(--fs-label);
	}
	.nows {
		margin: 0 8px 8px;
		flex: none;
	}
	.list {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 0 8px;
		flex: none;
	}
	.svc {
		padding: 10px 12px;
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		min-width: 0;
	}
	.nm {
		font-weight: 600;
		font-size: var(--fs-ui);
	}
	.acts {
		display: flex;
		gap: 5px;
		flex-wrap: wrap;
	}
	.desc {
		color: var(--text-muted);
		font-size: var(--fs-label);
	}
	.facts {
		--k: 64px;
	}
	.facts dt {
		color: var(--text-faint);
	}
	.probe {
		font-size: var(--fs-meta);
	}
	.ports {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.port {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		min-width: 0;
	}
	.pn {
		color: var(--accent-2-text);
	}
	.addr {
		font-size: var(--fs-meta);
		overflow-wrap: anywhere;
	}
	.lasterr {
		font-size: var(--fs-meta);
		color: var(--danger-text);
		background: var(--danger-bg);
		border: 1px solid var(--danger-line);
		border-radius: var(--r-row);
		padding: 5px 8px;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.logbar {
		display: flex;
		align-items: center;
		gap: 6px;
		margin-top: 2px;
	}
	.out {
		max-height: 260px;
		line-height: 1.65;
	}
	.out pre {
		margin: 0;
		font: inherit;
		white-space: pre-wrap;
		word-break: break-all;
	}
	.out > .empty-line {
		font-family: var(--font-body);
		padding: 0;
	}
	.foot {
		padding: 10px 12px;
	}
	.foot a {
		font-weight: 500;
	}
</style>
