<script lang="ts" module>
	import type { ServiceConfig } from '$lib/domain';
	import { parseGoDuration } from '$lib/ui/duration';

	export type ProbeKind = 'none' | 'http' | 'tcp' | 'command';

	/** One service as the form edits it: every field a string or a number
	 *  input's value, the probe as one choice. `key` only keys the list. */
	export interface ServiceRow {
		key: number;
		name: string;
		description: string;
		start: string;
		stop: string;
		probe: ProbeKind;
		http: string;
		tcp: number | '';
		command: string;
		timeout: string;
		interval: string;
		ports: { port: number | ''; label: string; path: string }[];
	}

	let KEY = 0;

	export function blankService(name = ''): ServiceRow {
		return {
			key: ++KEY,
			name,
			description: '',
			start: '',
			stop: '',
			probe: 'none',
			http: '',
			tcp: '',
			command: '',
			timeout: '',
			interval: '',
			ports: []
		};
	}

	export function serviceRows(svcs: ServiceConfig[]): ServiceRow[] {
		return svcs.map((s) => {
			const h = s.health;
			return {
				...blankService(s.name),
				description: s.description ?? '',
				start: s.start,
				stop: s.stop ?? '',
				probe: h?.http ? 'http' : h?.tcp ? 'tcp' : h?.command ? 'command' : 'none',
				http: h?.http ?? '',
				tcp: h?.tcp ?? '',
				command: h?.command ?? '',
				timeout: h?.timeout ?? '',
				interval: h?.interval ?? '',
				ports: s.ports.map((p) => ({ port: p.port, label: p.label ?? '', path: p.path ?? '' }))
			};
		});
	}

	/** The rows as the engine takes them: only the chosen probe, and its
	 *  durations only with a probe (they time nothing without one). */
	export function serviceConfigs(rows: ServiceRow[]): ServiceConfig[] {
		return rows.map((r) => {
			const probe =
				r.probe === 'http' && r.http.trim()
					? { http: r.http.trim() }
					: r.probe === 'tcp' && r.tcp !== ''
						? { tcp: Number(r.tcp) }
						: r.probe === 'command' && r.command.trim()
							? { command: r.command }
							: null;
			return {
				name: r.name.trim(),
				description: r.description.trim() || undefined,
				start: r.start,
				stop: r.stop.trim() ? r.stop : undefined,
				health: probe
					? {
							...probe,
							timeout: r.timeout.trim() || undefined,
							interval: r.interval.trim() || undefined
						}
					: undefined,
				ports: r.ports
					.filter((p) => p.port !== '' || p.label.trim() || p.path.trim())
					.map((p) => ({
						port: Number(p.port),
						label: p.label.trim() || undefined,
						path: p.path.trim() || undefined
					}))
			};
		});
	}

	const SLUG = /^[a-z][a-z0-9_-]*$/;
	const isPort = (v: number | ''): v is number =>
		v !== '' && Number.isInteger(v) && v >= 1 && v <= 65535;

	/** What the engine would refuse, per row — the same rules, said early.
	 *  The engine stays the authority: its answer on save is shown as is. */
	export function serviceIssues(rows: ServiceRow[]): Map<number, string[]> {
		const out = new Map<number, string[]>();
		const seen = new Map<string, number>();
		for (const r of rows) {
			const n = r.name.trim();
			if (n) seen.set(n, (seen.get(n) ?? 0) + 1);
		}
		for (const r of rows) {
			const bad: string[] = [];
			const n = r.name.trim();
			if (!SLUG.test(n)) bad.push('name');
			else if ((seen.get(n) ?? 0) > 1) bad.push('dup');
			if (!r.start.trim()) bad.push('start');
			if (r.probe === 'http') {
				let ok = false;
				try {
					const u = new URL(r.http.trim());
					ok = (u.protocol === 'http:' || u.protocol === 'https:') && !!u.host;
				} catch {
					/* not a URL */
				}
				if (!ok) bad.push('http');
			}
			if (r.probe === 'tcp' && !isPort(r.tcp)) bad.push('tcp');
			if (r.probe === 'command' && !r.command.trim()) bad.push('command');
			if (r.probe !== 'none') {
				for (const [f, v] of [
					['timeout', r.timeout],
					['interval', r.interval]
				] as const) {
					const s = v.trim() ? parseGoDuration(v) : 1;
					if (s === null || s <= 0) bad.push(f);
				}
			}
			const ports = new Set<number>();
			r.ports.forEach((p, i) => {
				if (p.port === '' && !p.label.trim() && !p.path.trim()) return;
				if (!isPort(p.port)) bad.push(`port${i}`);
				else if (ports.has(p.port)) bad.push(`portdup${i}`);
				else ports.add(p.port);
				if (p.path.trim() && !p.path.trim().startsWith('/')) bad.push(`path${i}`);
			});
			if (bad.length) out.set(r.key, bad);
		}
		return out;
	}
</script>

<script lang="ts">
	// The workshop's services, edited: each one's name (what a flow's
	// `service:` names), the script that starts it and the one that stops
	// it, the probe that says it is up, and the ports it listens on. A card
	// per service, folded to a one-line summary once it is set up.
	import { untrack } from 'svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import { I } from '$lib/ui/icons';

	let {
		rows = $bindable([]),
		issues,
		strict = false
	}: {
		rows: ServiceRow[];
		/** serviceIssues(rows), computed by the form that also gates Save. */
		issues: Map<number, string[]>;
		/** A save was tried: an empty start is called out too (before, a new
		 *  service is not scolded for a field nobody has reached yet). */
		strict?: boolean;
	} = $props();

	// Set-up services start folded (a lone one open); new ones open; one
	// with something to fix opens itself.
	let open = $state<Set<number>>(untrack(() => new Set(rows.length === 1 ? [rows[0].key] : [])));
	const isOpen = (r: ServiceRow) => open.has(r.key) || issues.has(r.key);
	function toggle(r: ServiceRow) {
		const next = new Set(open);
		if (next.has(r.key)) next.delete(r.key);
		else next.add(r.key);
		open = next;
	}

	function add() {
		// Flows reach the main service through `service: $app`, whose default
		// is `default`: the first one is offered that name.
		const r = blankService(rows.length === 0 ? 'default' : '');
		rows = [...rows, r];
		open = new Set([...open, r.key]);
		requestAnimationFrame(() =>
			document.getElementById(`svc-${r.key}-${rows.length === 1 ? 'start' : 'name'}`)?.focus()
		);
	}
	function remove(r: ServiceRow) {
		rows = rows.filter((x) => x.key !== r.key);
	}
	function move(r: ServiceRow, by: number) {
		const i = rows.findIndex((x) => x.key === r.key);
		const j = i + by;
		if (i < 0 || j < 0 || j >= rows.length) return;
		const next = [...rows];
		[next[i], next[j]] = [next[j], next[i]];
		rows = next;
	}

	const has = (r: ServiceRow, f: string) =>
		(issues.get(r.key)?.includes(f) ?? false) && (f !== 'start' || strict || !!r.start);
	const shownIssues = (r: ServiceRow) =>
		(issues.get(r.key) ?? []).filter((f) => f !== 'start' || strict || !!r.start);

	function probeSummary(r: ServiceRow): string {
		switch (r.probe) {
			case 'http':
				return r.http.trim() || t('form.services.probe.http');
			case 'tcp':
				return r.tcp !== '' ? `tcp ${r.tcp}` : t('form.services.probe.tcp');
			case 'command':
				return t('form.services.probe.command');
			default:
				return t('form.services.noProbe');
		}
	}
</script>

<div class="svcs">
	{#each rows as r, i (r.key)}
		{@const o = isOpen(r)}
		{@const bad = shownIssues(r).length > 0}
		<div class="svc card-in" class:invalid={bad}>
			<div class="sh">
				<button
					type="button"
					class="btn-icon fold"
					aria-expanded={o}
					aria-label={o
						? t('form.services.fold', { name: r.name || t('form.services.unnamed') })
						: t('form.services.unfold', { name: r.name || t('form.services.unnamed') })}
					onclick={() => toggle(r)}>{o ? '▾' : I.caret}</button
				>
				<span class="nm mono" class:fnt={!r.name.trim()}
					>{r.name.trim() || t('form.services.unnamed')}</span
				>
				{#if !o}
					<span class="sum ell">
						<span class="mono">{r.start.split('\n')[0]}</span>
						<span class="fnt">· {probeSummary(r)}</span>
						{#each r.ports.filter((p) => p.port !== '') as p, j (j)}
							<span class="pill sm mono t-mut">:{p.port}</span>
						{/each}
					</span>
				{:else if r.description.trim()}
					<span class="sum ell fnt">{r.description}</span>
				{/if}
				<span class="spacer"></span>
				{#if bad}<span class="pill sm t-ros">{t('form.services.toFix')}</span>{/if}
				<button
					type="button"
					class="btn-icon"
					disabled={i === 0}
					aria-label={t('form.services.moveUp', { name: r.name || t('form.services.unnamed') })}
					title={t('form.services.orderTitle')}
					onclick={() => move(r, -1)}>↑</button
				>
				<button
					type="button"
					class="btn-icon"
					disabled={i === rows.length - 1}
					aria-label={t('form.services.moveDown', { name: r.name || t('form.services.unnamed') })}
					title={t('form.services.orderTitle')}
					onclick={() => move(r, 1)}>↓</button
				>
				<button
					type="button"
					class="btn-icon"
					aria-label={t('form.services.remove', { name: r.name || t('form.services.unnamed') })}
					onclick={() => remove(r)}>{I.close}</button
				>
			</div>

			{#if o}
				<div class="body">
					<div class="grid2">
						<div class="field">
							<label for="svc-{r.key}-name">{t('form.services.name')}</label>
							<input
								id="svc-{r.key}-name"
								class="mono"
								class:invalid={has(r, 'name') || has(r, 'dup')}
								bind:value={r.name}
								placeholder={i === 0 ? 'default' : 'db'}
								autocomplete="off"
								spellcheck="false"
							/>
							{#if has(r, 'name')}
								<div class="err">{t('form.services.badName')}</div>
							{:else if has(r, 'dup')}
								<div class="err">{t('form.services.dupName', { name: r.name.trim() })}</div>
							{:else}
								<div class="help">
									{@html th('form.services.nameHelp', { name: r.name.trim() || 'default' })}
								</div>
							{/if}
						</div>
						<div class="field">
							<label for="svc-{r.key}-desc">{t('form.services.description')}</label>
							<input
								id="svc-{r.key}-desc"
								bind:value={r.description}
								placeholder={t('form.services.descriptionPh')}
							/>
						</div>
					</div>

					<div class="grid2">
						<div class="field">
							<label for="svc-{r.key}-start">{t('form.services.start')}</label>
							<textarea
								id="svc-{r.key}-start"
								class="mono"
								class:invalid={has(r, 'start')}
								rows="3"
								bind:value={r.start}
								placeholder="npm run dev"
								spellcheck="false"></textarea>
							{#if has(r, 'start')}<div class="err">{t('form.services.startRequired')}</div>{/if}
						</div>
						<div class="field">
							<label for="svc-{r.key}-stop"
								>{t('form.services.stop')}
								<span class="fnt">{t('form.services.optional')}</span></label
							>
							<textarea
								id="svc-{r.key}-stop"
								class="mono"
								rows="3"
								bind:value={r.stop}
								placeholder="docker compose down"
								spellcheck="false"></textarea>
						</div>
					</div>
					<div class="help">{@html th('form.services.startHelp')}</div>

					<div class="grp">
						<div class="row gh">
							<span class="lbl">{t('form.services.health')}</span>
							<Segmented
								glass={false}
								size="sm"
								label={t('form.services.health')}
								bind:value={r.probe}
								options={[
									{ value: 'none', label: t('form.services.probe.none') },
									{ value: 'http', label: t('form.services.probe.http') },
									{ value: 'tcp', label: t('form.services.probe.tcp') },
									{ value: 'command', label: t('form.services.probe.command') }
								]}
							/>
						</div>
						{#if r.probe === 'none'}
							<div class="help">{t('form.services.noneHelp')}</div>
						{:else}
							<div class="probe">
								<div class="field pf">
									{#if r.probe === 'http'}
										<label for="svc-{r.key}-http">{t('form.services.httpLabel')}</label>
										<input
											id="svc-{r.key}-http"
											class="mono"
											class:invalid={has(r, 'http')}
											bind:value={r.http}
											placeholder="http://localhost:3000/health"
											spellcheck="false"
										/>
										<div class={has(r, 'http') ? 'err' : 'help'}>
											{t(has(r, 'http') ? 'form.services.badHttp' : 'form.services.httpHelp')}
										</div>
									{:else if r.probe === 'tcp'}
										<label for="svc-{r.key}-tcp">{t('form.services.tcpLabel')}</label>
										<input
											id="svc-{r.key}-tcp"
											class="mono"
											class:invalid={has(r, 'tcp')}
											type="number"
											min="1"
											max="65535"
											bind:value={r.tcp}
											placeholder="5432"
										/>
										<div class={has(r, 'tcp') ? 'err' : 'help'}>
											{t(has(r, 'tcp') ? 'form.services.badPort' : 'form.services.tcpHelp')}
										</div>
									{:else}
										<label for="svc-{r.key}-cmd">{t('form.services.commandLabel')}</label>
										<textarea
											id="svc-{r.key}-cmd"
											class="mono"
											class:invalid={has(r, 'command')}
											rows="2"
											bind:value={r.command}
											placeholder="pg_isready -h localhost"
											spellcheck="false"></textarea>
										<div class={has(r, 'command') ? 'err' : 'help'}>
											{t(
												has(r, 'command')
													? 'form.services.commandRequired'
													: 'form.services.commandHelp'
											)}
										</div>
									{/if}
								</div>
								<div class="field dur">
									<label for="svc-{r.key}-timeout">{t('form.services.timeout')}</label>
									<input
										id="svc-{r.key}-timeout"
										class="mono"
										class:invalid={has(r, 'timeout')}
										bind:value={r.timeout}
										placeholder="5m"
										spellcheck="false"
									/>
									<div class={has(r, 'timeout') ? 'err' : 'help'}>
										{t(
											has(r, 'timeout') ? 'form.services.badDuration' : 'form.services.timeoutHelp'
										)}
									</div>
								</div>
								<div class="field dur">
									<label for="svc-{r.key}-interval">{t('form.services.interval')}</label>
									<input
										id="svc-{r.key}-interval"
										class="mono"
										class:invalid={has(r, 'interval')}
										bind:value={r.interval}
										placeholder="10s"
										spellcheck="false"
									/>
									<div class={has(r, 'interval') ? 'err' : 'help'}>
										{t(
											has(r, 'interval')
												? 'form.services.badDuration'
												: 'form.services.intervalHelp'
										)}
									</div>
								</div>
							</div>
						{/if}
					</div>

					<div class="grp">
						<div class="row gh">
							<span class="lbl">{t('form.services.ports')}</span>
							<span class="count">{r.ports.length}</span>
							<span class="spacer"></span>
							<button
								type="button"
								class="btn-sm"
								onclick={() => (r.ports = [...r.ports, { port: '', label: '', path: '' }])}
								>{I.add} {t('form.services.addPort')}</button
							>
						</div>
						{#if r.ports.length}
							<div class="ports">
								{#each r.ports as p, j (j)}
									<div class="pr">
										<input
											class="mono"
											class:invalid={has(r, `port${j}`) || has(r, `portdup${j}`)}
											type="number"
											min="1"
											max="65535"
											bind:value={p.port}
											placeholder="3000"
											aria-label={t('form.services.portAria')}
										/>
										<input
											bind:value={p.label}
											placeholder={t('form.services.labelPh')}
											aria-label={t('form.services.labelAria')}
										/>
										<input
											class="mono"
											class:invalid={has(r, `path${j}`)}
											bind:value={p.path}
											placeholder="/"
											aria-label={t('form.services.pathAria')}
											spellcheck="false"
										/>
										<button
											type="button"
											class="btn-icon"
											aria-label={t('form.services.removePort', { port: p.port || '?' })}
											onclick={() => (r.ports = r.ports.filter((_, k) => k !== j))}
											>{I.close}</button
										>
									</div>
									{#if has(r, `port${j}`)}<div class="err">{t('form.services.badPort')}</div>{/if}
									{#if has(r, `portdup${j}`)}<div class="err">
											{t('form.services.dupPort', { port: p.port })}
										</div>{/if}
									{#if has(r, `path${j}`)}<div class="err">{t('form.services.badPath')}</div>{/if}
								{/each}
							</div>
						{/if}
						<div class="help">
							{@html th('form.services.portsHelp', { name: r.name.trim() || 'default' })}
						</div>
					</div>
				</div>
			{/if}
		</div>
	{/each}
	<div class="row">
		<button type="button" class="btn-sm" onclick={add}
			>{I.add} {rows.length ? t('form.services.addAnother') : t('form.services.add')}</button
		>
	</div>
</div>

<style>
	.svcs {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin-top: 4px;
	}
	.svc {
		overflow: hidden;
	}
	.svc.invalid {
		border-color: var(--rosLn);
	}
	.sh {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 6px 8px;
		min-width: 0;
	}
	.fold {
		width: 22px;
	}
	.nm {
		font-weight: 600;
		font-size: var(--fs-ui);
		flex: none;
	}
	.sum {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		font-size: var(--fs-label);
		color: var(--mut);
	}
	.sum .mono {
		font-size: var(--fs-meta);
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: 12px;
		padding: 10px 12px 14px;
		border-top: 1px solid var(--ln);
	}
	.body textarea {
		resize: vertical;
		min-height: 54px;
	}
	.grp {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.gh {
		gap: 10px;
	}
	.probe {
		display: grid;
		grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) minmax(0, 1fr);
		gap: 10px 14px;
		align-items: start;
	}
	.ports {
		display: flex;
		flex-direction: column;
		gap: 4px;
		max-width: 620px;
	}
	.pr {
		display: grid;
		grid-template-columns: 110px minmax(0, 1fr) 140px 28px;
		gap: 6px;
		align-items: center;
	}
	@media (max-width: 760px) {
		.probe {
			grid-template-columns: minmax(0, 1fr);
		}
		.pr {
			grid-template-columns: 90px minmax(0, 1fr) 28px;
		}
		.pr > :nth-child(3) {
			grid-column: 1 / 3;
		}
	}
</style>
