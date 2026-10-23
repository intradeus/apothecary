<script lang="ts">
	// Workshop automations tab: how runs begin without a person pressing
	// Start. The workshop's rules with what the watcher last did with each,
	// a pause toggle, and a form the server validates the way a firing
	// would be — so a refusal names what the watcher could not run.
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type {
		Automation,
		AutomationDraft,
		AutomationKind,
		Integration,
		Project,
		Ticket
	} from '$lib/domain';
	import { ago, fmtAbs } from '$lib/format';
	import FlowSelect from '$lib/FlowSelect.svelte';
	import StartRunOptions from '$lib/run/StartRunOptions.svelte';
	import Modal from '$lib/ui/Modal.svelte';
	import Segmented from '$lib/ui/Segmented.svelte';
	import Toggle from '$lib/ui/Toggle.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';

	let {
		project,
		tickets,
		integrations
	}: { project: Project; tickets: Ticket[] | null; integrations: Integration[] | null } = $props();

	let rows = $state<Automation[] | null>(null);
	let error = $state('');
	const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));
	async function load() {
		try {
			rows = await api.automations.list(project.id);
			error = '';
		} catch (e) {
			error = msg(e);
		}
	}
	$effect(() => {
		void project.id;
		void load();
	});

	const sources = $derived((integrations ?? []).filter((i) => i.port === 'ticketsource'));
	const ticketTitle = (id: string) => (tickets ?? []).find((tk) => tk.id === id)?.title ?? id;
	const sourceName = (id: string) => sources.find((s) => s.id === id)?.name ?? id;
	const kindLabel = (k: AutomationKind) =>
		k === 'cron'
			? t('workshop.automations.kindCron')
			: k === 'forge_mr'
				? t('workshop.automations.kindMR')
				: t('workshop.automations.kindTracker');
	const KINDS = $derived<{ value: AutomationKind; label: string }[]>([
		{ value: 'tracker_query', label: t('workshop.automations.kindTracker') },
		{ value: 'cron', label: t('workshop.automations.kindCron') },
		{ value: 'forge_mr', label: t('workshop.automations.kindMR') }
	]);
	// The repositories a forge_mr automation may watch: the primary, then
	// the extras by name.
	const repoNames = $derived(['', ...(project.repos ?? []).map((r) => r.name)]);
	const repoLabel = (r: string) => (r ? r : t('workshop.automations.repoPrimary'));

	// --- the form ---
	let open = $state(false);
	let editing = $state<Automation | null>(null);
	let draft = $state<AutomationDraft>(blank());
	let flowPick = $state(''); // '' = the workshop's default flow
	let phase = $state('');
	let params = $state<Record<string, string>>({});
	let busy = $state(false);
	let formError = $state('');
	const flowName = $derived(flowPick || project.defaultFlow);

	function blank(): AutomationDraft {
		return { name: '', kind: 'tracker_query', flow: '', enabled: true };
	}
	function startNew() {
		editing = null;
		draft = blank();
		draft.sourceId = sources[0]?.id;
		flowPick = '';
		phase = '';
		params = {};
		formError = '';
		open = true;
	}
	function startEdit(a: Automation) {
		editing = a;
		draft = {
			name: a.name,
			kind: a.kind,
			sourceId: a.sourceId,
			query: a.query,
			schedule: a.schedule,
			ticketId: a.ticketId,
			repo: a.repo,
			flow: a.flow,
			enabled: a.enabled
		};
		flowPick = a.flow === project.defaultFlow ? '' : a.flow;
		phase = a.phase ?? '';
		params = { ...a.params };
		formError = '';
		open = true;
	}
	function draftOf(a: Automation, enabled: boolean): AutomationDraft {
		return {
			name: a.name,
			kind: a.kind,
			sourceId: a.sourceId,
			query: a.query,
			schedule: a.schedule,
			ticketId: a.ticketId,
			repo: a.repo,
			flow: a.flow,
			phase: a.phase,
			params: a.params,
			enabled
		};
	}

	async function save(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		formError = '';
		const d: AutomationDraft = { ...draft, flow: flowName, phase: phase || undefined, params };
		// Only what the kind takes travels.
		if (d.kind !== 'tracker_query') {
			d.sourceId = undefined;
			d.query = undefined;
		}
		if (d.kind !== 'cron') {
			d.schedule = undefined;
			d.ticketId = undefined;
		}
		if (d.kind !== 'forge_mr') d.repo = undefined;
		try {
			const saved = editing
				? await api.automations.update(project.id, editing.id, d)
				: await api.automations.create(project.id, d);
			open = false;
			toast(t('workshop.automations.saved', { name: saved.name }));
			await load();
		} catch (err) {
			formError = msg(err);
		} finally {
			busy = false;
		}
	}

	async function toggle(a: Automation, enabled: boolean) {
		try {
			await api.automations.update(project.id, a.id, draftOf(a, enabled));
			await load();
		} catch (err) {
			toast(msg(err));
		}
	}

	async function remove(a: Automation) {
		const ok = await confirmDialog({
			title: t('workshop.automations.deleteTitle', { name: a.name }),
			body: t('workshop.automations.deleteBody'),
			action: t('workshop.automations.deleteAction')
		});
		if (!ok) return;
		try {
			await api.automations.remove(project.id, a.id);
			toast(t('workshop.automations.deleted', { name: a.name }));
			await load();
		} catch (err) {
			toast(msg(err));
		}
	}
</script>

<div class="row nowrap">
	<p class="lede grow">{t('workshop.automations.lede')}</p>
	<button class="btn-primary" onclick={startNew}>{I.add} {t('workshop.automations.new')}</button>
</div>

{#if error && !rows}
	<ErrorCard cause={t('workshop.automations.loadError')} detail={error} onretry={load} />
{:else if !rows}
	<Skeleton pane lines={4} />
{:else if !rows.length}
	<section class="c pane">
		<div class="pane-body">
			<EmptyState
				compact
				title={t('workshop.automations.emptyTitle')}
				text={t('workshop.automations.emptyText')}
			>
				<button class="btn-primary" onclick={startNew}
					>{I.add} {t('workshop.automations.new')}</button
				>
			</EmptyState>
		</div>
	</section>
{:else}
	<section class="c pane">
		<div class="pane-head">
			<h2>{t('page.workshop.tabAutomations')}</h2>
			<span class="count">{rows.length}</span>
		</div>
		<div class="rows">
			<div class="r head ar">
				<span>{t('workshop.automations.colName')}</span><span
					>{t('workshop.automations.colFires')}</span
				><span>{t('workshop.automations.colRuns')}</span><span class="num"
					>{t('workshop.automations.colLast')}</span
				><span></span>
			</div>
			{#each rows as a (a.id)}
				<div class="r ar" class:dim={!a.enabled}>
					<span class="nm">
						<Toggle
							checked={a.enabled}
							title={a.enabled
								? t('workshop.automations.enabled')
								: t('workshop.automations.disabled')}
							onchange={(v) => toggle(a, v)}
						/>
						<span class="name">{a.name}</span>
						<span class="pill sm t-mut">{kindLabel(a.kind)}</span>
					</span>
					<span class="mono small fires">
						{#if a.kind === 'cron'}
							{a.schedule} · {ticketTitle(a.ticketId ?? '')}
						{:else if a.kind === 'forge_mr'}
							{t('workshop.automations.mrSummary', { repo: repoLabel(a.repo ?? '') })}
						{:else}
							{sourceName(a.sourceId ?? '')}{a.query ? ` · ${a.query}` : ''}
						{/if}
					</span>
					<span class="small">
						{a.phase
							? t('workshop.automations.flowPhase', { flow: a.flow, phase: a.phase })
							: t('workshop.automations.flowFirst', { flow: a.flow })}
						{#if Object.keys(a.params).length}
							· {t('workshop.automations.overrides', { count: Object.keys(a.params).length })}
						{/if}
					</span>
					<span class="num small last">
						{#if a.lastFiredAt}
							<span title={fmtAbs(a.lastFiredAt)}
								>{t('workshop.automations.fired', { ago: ago(a.lastFiredAt) })}</span
							>
						{:else if a.lastPolledAt}
							<span title={fmtAbs(a.lastPolledAt)}
								>{t('workshop.automations.polled', { ago: ago(a.lastPolledAt) })}</span
							>
						{:else}
							<span class="fnt">{t('workshop.automations.never')}</span>
						{/if}
						{#if a.lastError}<span class="hue-amb err-line" title={a.lastError}>{a.lastError}</span
							>{/if}
					</span>
					<span class="acts">
						<button class="btn-sm" onclick={() => startEdit(a)}
							>{t('workshop.automations.edit')}</button
						>
						<button class="btn-sm" onclick={() => remove(a)}
							>{t('workshop.automations.delete')}</button
						>
					</span>
				</div>
			{/each}
		</div>
	</section>
{/if}

{#if open}
	<Modal
		bind:open
		title={editing
			? t('workshop.automations.formEdit', { name: editing.name })
			: t('workshop.automations.formNew')}
		width={640}
		onclose={() => (open = false)}
	>
		<form id="automation-form" class="af" onsubmit={save}>
			<div class="field">
				<label for="au-name">{t('workshop.automations.name')}</label>
				<input id="au-name" bind:value={draft.name} required />
			</div>
			<div class="field">
				<span class="lbl">{t('workshop.automations.kind')}</span>
				<Segmented
					options={KINDS}
					value={draft.kind}
					size="sm"
					glass={false}
					label={t('workshop.automations.kind')}
					onchange={(v) => (draft.kind = v)}
				/>
			</div>
			{#if draft.kind === 'tracker_query'}
				<div class="field">
					<label for="au-source">{t('workshop.automations.source')}</label>
					{#if sources.length}
						<select id="au-source" bind:value={draft.sourceId}>
							{#each sources as s (s.id)}<option value={s.id}>{s.name} · {s.kind}</option>{/each}
						</select>
					{:else}
						<div class="help hue-amb">{t('workshop.automations.noSources')}</div>
					{/if}
					<span class="help">{t('workshop.automations.sourceHelp')}</span>
				</div>
				<div class="field">
					<label for="au-query">{t('workshop.automations.query')}</label>
					<input id="au-query" class="mono" bind:value={draft.query} />
					<span class="help">{t('workshop.automations.queryHelp')}</span>
				</div>
			{:else if draft.kind === 'forge_mr'}
				<div class="field">
					<label for="au-repo">{t('workshop.automations.repo')}</label>
					<select id="au-repo" bind:value={draft.repo}>
						{#each repoNames as r (r)}<option value={r || undefined}>{repoLabel(r)}</option>{/each}
					</select>
					<span class="help">{t('workshop.automations.repoHelp')}</span>
				</div>
			{:else}
				<div class="field">
					<label for="au-schedule">{t('workshop.automations.schedule')}</label>
					<input
						id="au-schedule"
						class="mono"
						bind:value={draft.schedule}
						placeholder="0 9 * * 1-5"
					/>
					<span class="help">{t('workshop.automations.scheduleHelp')}</span>
				</div>
				<div class="field">
					<label for="au-ticket">{t('workshop.automations.ticket')}</label>
					{#if tickets?.length}
						<select id="au-ticket" bind:value={draft.ticketId}>
							<option value="">—</option>
							{#each tickets as tk (tk.id)}<option value={tk.id}>{tk.title}</option>{/each}
						</select>
					{:else}
						<div class="help hue-amb">{t('workshop.automations.noTickets')}</div>
					{/if}
					<span class="help">{t('workshop.automations.ticketHelp')}</span>
				</div>
			{/if}
			<div class="field">
				<label for="au-flow">{t('workshop.automations.flow')}</label>
				<FlowSelect
					id="au-flow"
					projectId={project.id}
					bind:value={flowPick}
					fallbackLabel={t('workshop.automations.defaultFlow', { flow: project.defaultFlow })}
				/>
			</div>
			<StartRunOptions
				projectId={project.id}
				{flowName}
				bind:phase
				bind:params
				entries={draft.kind === 'forge_mr' ? 'mr' : 'fresh'}
				askMR={false}
				idPrefix="au"
			/>
			<Toggle
				checked={draft.enabled}
				label={t('workshop.automations.startEnabled')}
				onchange={(v) => (draft.enabled = v)}
			/>
			{#if formError}<div class="err">{formError}</div>{/if}
		</form>
		{#snippet footer()}
			<button type="button" class="btn-ghost" onclick={() => (open = false)}
				>{t('common.cancel')}</button
			>
			<button
				class="btn-primary"
				type="submit"
				form="automation-form"
				disabled={busy || !draft.name.trim()}
				aria-busy={busy}
				>{busy
					? t('workshop.automations.saving')
					: editing
						? t('workshop.automations.save')
						: t('workshop.automations.create')}</button
			>
		{/snippet}
	</Modal>
{/if}

<style>
	.ar {
		grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr) minmax(0, 1fr) 170px auto;
	}
	.nm {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.nm .name {
		font-weight: 600;
		color: var(--tx);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.fires {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.dim {
		opacity: var(--o-dim);
	}
	.last {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 2px;
	}
	.err-line {
		display: block;
		max-width: 170px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.acts {
		display: flex;
		gap: 6px;
		justify-content: flex-end;
	}
	.af {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.lbl {
		display: block;
		margin-bottom: 6px;
	}
</style>
