<script lang="ts">
	import { errorMessage } from '$lib/core/errors';
	import { to } from '$lib/core/urls';
	import { renderBranch, slug } from '$lib/core/branchname';
	import { t, th } from '$lib/i18n/index.svelte';
	// Ticket create/edit form. A ticket is a unit of work; flow is NOT chosen
	// here — it's picked per run when a run is started.
	//
	// A new ticket can also be IMPORTED from a tracker (Jira): pick a source,
	// search it, and the chosen issue prefills this form. From then on the
	// ticket mirrors the issue: title, goal and change type follow the tracker,
	// so editing a sourced ticket leaves them read-only.
	import { api } from '$lib/api';
	import type {
		Attachment,
		ChangeType,
		Integration,
		Project,
		SourceTicket,
		Ticket
	} from '$lib/domain';
	import { CHANGE_TYPES, INTERNAL_TRACKER } from '$lib/domain';
	import FileChips from '$lib/artifacts/FileChips.svelte';
	import AttachFiles from '$lib/artifacts/AttachFiles.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import FileChip from '$lib/ui/FileChip.svelte';
	import SearchInput from '$lib/ui/SearchInput.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/design/icons';
	import { onMount } from 'svelte';

	let {
		projects,
		existing = null,
		onsaved,
		initialProject = '',
		oncancel
	}: {
		projects: Project[];
		existing?: Ticket | null;
		onsaved: (t: Ticket) => void;
		/** Preselect this workshop on a new ticket (e.g. from ?project=). */
		initialProject?: string;
		/** Show a Cancel button that calls this. */
		oncancel?: () => void;
	} = $props();

	// svelte-ignore state_referenced_locally
	const init = $state.snapshot(existing) as Ticket | null;
	/** A sourced ticket: its tracker owns the title, the goal and the type. */
	const tracked = !!init?.source;

	// svelte-ignore state_referenced_locally
	let projectId = $state(
		init?.projectId ?? (projects.some((p) => p.id === initialProject) ? initialProject : '')
	);
	// svelte-ignore state_referenced_locally
	if (!projectId) projectId = projects[projects.length - 1]?.id ?? '';
	let title = $state(init?.title ?? '');
	let goal = $state(init?.goal ?? '');
	let branch = $state(init?.baseBranch ?? '');
	let files = $state<File[]>([]);

	// --- import from a tracker (create only) ---
	let sources = $state<Integration[]>([]);
	let sourceId = $state(''); // '' = write it by hand
	let query = $state('');
	let results = $state<SourceTicket[]>([]);
	let searching = $state(false);
	let searchErr = $state('');
	let picked = $state<SourceTicket | null>(null); // the issue being copied

	// Already-attached files, shown when editing so the list isn't invisible
	// state the form silently round-trips.
	let attached = $state<Attachment[]>([]);

	onMount(async () => {
		if (init) {
			// Provenance is immutable, so editing never re-imports — but it should
			// still show what the ticket carries.
			api.tickets
				.attachments(init.id)
				.then((a) => (attached = a))
				.catch(() => {});
			return;
		}
		try {
			sources = (await api.integrations.list()).filter(
				(i) => i.port === 'ticketsource' && i.kind !== INTERNAL_TRACKER
			);
		} catch {
			/* no tracker configured is the normal case */
		}
	});

	async function search() {
		if (!sourceId) return;
		searching = true;
		searchErr = '';
		try {
			results = await api.integrations.sourceTickets(sourceId, query.trim());
		} catch (e) {
			searchErr = errorMessage(e);
			results = [];
		} finally {
			searching = false;
		}
	}

	function openSource(id: string) {
		sourceId = id;
		results = [];
		searchErr = '';
		search(); // the empty query is the tracker's own "what's on my plate"
	}

	// Picking an issue fills the form from the full issue (search results carry
	// no attachments, so this re-fetches). Everything stays editable — the
	// import is a starting point, not a lock.
	async function pick(st: SourceTicket) {
		searchErr = '';
		try {
			picked = await api.integrations.sourceTicket(sourceId, st.key);
		} catch (e) {
			searchErr = errorMessage(e);
			return;
		}
		title = picked.title ?? '';
		goal = picked.body ?? '';
		changeType = picked.changeType || 'feat';
		results = [];
		query = '';
	}

	function clearImport() {
		picked = null;
		sourceId = '';
		results = [];
		query = '';
	}

	const sourceName = (id: string) => sources.find((s) => s.id === id)?.name ?? id;
	const projectOptions = $derived<SelectOption[]>(
		projects.map((p) => ({ value: p.id, label: p.name }))
	);
	const sourceOptions = $derived<SelectOption[]>([
		{ value: '', label: t('form.ticket.chooseTracker') },
		...sources.map((s) => ({ value: s.id, label: `${s.name} (${s.kind})` }))
	]);

	// The ticket's base branch is named by its workshop's branch format
	// ($lib/core/branchname: {ticket_type}/{ticket_number}-{ticket_title} by
	// default, so feat/proj-123-add-footnotes) from its change type, the
	// picked issue's key (a hand-written ticket has none) and the title — the
	// name the engine gives a ticket created without one, an automation's
	// import included. It is prefilled, and follows the workshop, the type and
	// the title, until the user edits it; changing the type then swaps a
	// leading type in the field — runs already started keep theirs.
	let branchEdited = $state(init != null);
	let changeType = $state<ChangeType>(init?.changeType ?? 'feat');
	const typeOptions: SelectOption<ChangeType>[] = CHANGE_TYPES.map((c) => ({ value: c, label: c }));
	const branchFormat = $derived(projects.find((p) => p.id === projectId)?.repo.branchFormat ?? '');
	// Swap a branch's leading change type; any other name is left alone.
	function withType(b: string, type: string) {
		const [head, ...rest] = b.split('/');
		return rest.length && CHANGE_TYPES.includes(head as ChangeType) ? [type, ...rest].join('/') : b;
	}
	// A hand-written ticket with no title yet has nothing to be named by: no
	// name yet, rather than the engine's last-resort "ticket".
	function autoBranch() {
		if (!picked && !slug(title)) return '';
		return renderBranch(branchFormat, changeType, picked?.key ?? '', title);
	}
	$effect(() => {
		if (!branchEdited) branch = autoBranch();
	});

	let submitting = $state(false);
	let error = $state('');
	// An import that dropped an attachment stops here with the reason rather
	// than navigating away as if the copy were complete; the ticket exists
	// either way, so the operator moves on when they've read it.
	let warnings = $state<string[]>([]);
	let created = $state<Ticket | null>(null);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (submitting) return; // busy: Enter in a field must not send it twice
		submitting = true;
		error = '';
		warnings = [];
		try {
			let attachmentIds = init?.attachmentIds ?? [];
			if (files.length) {
				const uploaded = (await api.files.upload(files)).map((a) => a.id);
				attachmentIds = [...attachmentIds, ...uploaded];
			}
			const body = {
				title: title.trim(),
				goal: goal.trim(),
				projectId,
				branch: branch.trim() || undefined,
				changeType,
				attachmentIds,
				...(picked ? { sourceIntegrationId: sourceId, sourceKey: picked.key } : {})
			};
			const saved = init ? await api.tickets.update(init.id, body) : await api.tickets.create(body);
			if (saved.warnings?.length) {
				warnings = saved.warnings;
				created = saved;
				return;
			}
			toast(init ? t('form.ticket.saved') : t('form.ticket.created'));
			onsaved(saved);
		} catch (err) {
			error = errorMessage(err);
		} finally {
			submitting = false;
		}
	}
</script>

<form class="tf" class:edit={!!init} onsubmit={submit}>
	<section class="main glass pane">
		{#if projects.length === 0}
			<Callout hue="warning" glyph={I.warn}>
				{@html th('form.ticket.noWorkshops', {
					integrationsHref: to.system('integrations'),
					workshopHref: to.newWorkshop()
				})}
			</Callout>
		{/if}

		<div class="field">
			<label for="t-project">{t('form.ticket.workshop')}</label>
			<Select
				id="t-project"
				options={projectOptions}
				bind:value={projectId}
				required
				disabled={init != null}
			/>
			{#if init}<span class="help">{t('form.ticket.locked')}</span>{/if}
		</div>

		<div class="field">
			<label for="t-title">{t('form.ticket.title')}</label>
			<input
				id="t-title"
				bind:value={title}
				required
				disabled={tracked}
				placeholder={t('form.ticket.titlePlaceholder')}
			/>
			{#if tracked}<span class="help"
					>{t('form.ticket.trackerOwns', { kind: init?.source?.kind ?? '' })}</span
				>{/if}
		</div>

		<div class="field">
			<label for="t-goal">{t('form.ticket.goal')}</label>
			<span class="help">{t('form.ticket.goalHelp')}</span>
			<textarea
				id="t-goal"
				class="goal"
				bind:value={goal}
				required
				disabled={tracked}
				rows="10"
				placeholder={t('form.ticket.goalPlaceholder')}></textarea>
		</div>

		<div class="field">
			<div class="lrow">
				<label for="t-branch">{t('form.ticket.baseBranch')}</label>
				{#if branchEdited && !init}
					<span class="pill sm tint-warning">{t('form.ticket.edited')}</span>
					<button
						type="button"
						class="btn-ghost btn-sm"
						title={t('form.ticket.autoTitle')}
						onclick={() => {
							branchEdited = false;
							branch = autoBranch();
						}}>{I.retry} {t('form.ticket.auto')}</button
					>
				{:else if !init}
					<span class="pill sm tint-muted">{t('form.ticket.auto')}</span>
				{/if}
			</div>
			<div class="brow">
				<Select
					options={typeOptions}
					bind:value={changeType}
					onchange={(v) => {
						if (branchEdited) branch = withType(branch, v);
					}}
					inline
					mono
					label={t('form.ticket.changeType')}
					disabled={tracked}
					title={t('form.ticket.changeTypeTitle')}
				/>
				<input
					id="t-branch"
					bind:value={branch}
					oninput={() => (branchEdited = true)}
					placeholder={renderBranch(branchFormat, changeType, 'PROJ-123', 'Short description')}
					class="mono"
				/>
			</div>
			<span class="help">{t('form.ticket.branchHelp')}</span>
		</div>

		<div class="field">
			<span class="field-label">{t('form.ticket.attachments')}</span>
			{#if attached.length}
				<FileChips
					items={attached.map((a) => ({ ...a, kind: 'attachment' as const }))}
					label={t('form.ticket.alreadyAttached')}
				/>
			{/if}
			<AttachFiles bind:files label={t('form.ticket.dropLabel')} />
		</div>

		{#if error}<div class="error">{error}</div>{/if}
		{#if warnings.length && created}
			<Callout hue="warning" glyph={I.warn}>
				<b>{t('form.ticket.warnHead')}</b>
				<ul>
					{#each warnings as w (w)}<li>{w}</li>{/each}
				</ul>
			</Callout>
			<div class="form-actions">
				<button class="btn-primary" type="button" onclick={() => onsaved(created!)}
					>{t('form.ticket.continue')}</button
				>
			</div>
		{:else}
			<div class="form-actions">
				{#if oncancel}<button type="button" class="btn-ghost" onclick={oncancel}
						>{t('common.cancel')}</button
					>{/if}
				<button
					class="btn-primary btn-lg"
					type="submit"
					aria-busy={submitting}
					disabled={!title.trim() || !goal.trim() || !projectId}
				>
					{init
						? t('form.ticket.save')
						: picked
							? t('form.ticket.createFrom', { key: picked.key })
							: t('form.ticket.create')}
				</button>
			</div>
		{/if}
	</section>

	{#if !init}
		<aside class="imp glass pane">
			<div class="pane-head ih">
				<h2>{t('form.ticket.import')}</h2>
			</div>
			<div class="pane-body ib">
				<p class="lede">{t('form.ticket.importLede')}</p>
				{#if sources.length === 0}
					<div class="note">
						{t('form.ticket.noSource')}
						<a href={to.system('integrations')}>{t('form.ticket.addTracker')} {I.forward}</a>
					</div>
				{:else if picked}
					<div class="card-in picked">
						<div class="prow">
							<span class="tag">{picked.key}</span>
							<span class="truncate grow">{picked.title}</span>
						</div>
						<div class="help">{t('form.ticket.from', { name: sourceName(sourceId) })}</div>
						<!-- Name every file that will be copied — and say so plainly when the
						     issue has none, so "where are my attachments?" is answered before
						     the ticket is created rather than after. -->
						{#if picked.attachments?.length}
							<div class="help">
								{t('form.ticket.copied', { count: picked.attachments.length })}
							</div>
							<div class="chips">
								{#each picked.attachments as a (a.filename)}
									<FileChip name={a.filename} size={a.size} />
								{/each}
							</div>
						{:else}
							<div class="help">{t('form.ticket.noAttach')}</div>
						{/if}
						<div class="prow">
							{#if picked.url}
								<a class="btn btn-sm" href={picked.url} target="_blank" rel="noreferrer"
									>{t('form.ticket.openTracker')} {I.external}</a
								>
							{/if}
							<button type="button" class="btn-ghost btn-sm" onclick={clearImport}
								>{t('form.ticket.clear')}</button
							>
						</div>
					</div>
				{:else}
					<div class="field">
						<label for="t-src">{t('form.ticket.tracker')}</label>
						<Select
							id="t-src"
							options={sourceOptions}
							value={sourceId}
							onchange={(v) => {
								if (v) openSource(v);
								else clearImport();
							}}
						/>
					</div>
					{#if sourceId}
						<!-- Enter searches; it must never submit the ticket form around it. -->
						<!-- svelte-ignore a11y_no_static_element_interactions -->
						<div class="srow" onkeydown={(e) => e.key === 'Enter' && e.preventDefault()}>
							<SearchInput
								bind:value={query}
								width={400}
								placeholder={t('form.ticket.searchPlaceholder')}
								label={t('form.ticket.searchLabel')}
								onenter={() => search()}
							/>
							<button type="button" onclick={search} aria-busy={searching}
								>{t('form.ticket.search')}</button
							>
						</div>
						{#if searchErr}<div class="error">{searchErr}</div>{/if}
						{#if searching && !results.length}
							<div class="help">{t('form.ticket.searching')}</div>
						{:else if results.length}
							<div class="card-in results">
								{#each results as r (r.key)}
									<button type="button" class="rowbtn result" onclick={() => pick(r)}>
										<span class="rl1">
											<span class="tag">{r.key}</span>
											<span class="truncate rtitle">{r.title}</span>
										</span>
										<span class="rl2 truncate">
											{[r.type, r.status, r.assignee].filter(Boolean).join(' · ')}
										</span>
									</button>
								{/each}
							</div>
						{:else if !searchErr}
							<div class="help">{t('form.ticket.noMatch')}</div>
						{/if}
					{/if}
				{/if}
			</div>
		</aside>
	{/if}
</form>

<style>
	.tf {
		display: grid;
		grid-template-columns: minmax(0, 1fr) var(--aside-w);
		gap: 12px;
		align-items: start;
	}
	.tf.edit {
		grid-template-columns: minmax(0, 1fr);
	}
	.main {
		padding: 16px 18px 18px;
		display: flex;
		flex-direction: column;
		gap: 14px;
		min-width: 0;
	}
	.imp {
		position: sticky;
		top: 0;
		min-width: 0;
	}
	.ib {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.goal {
		min-height: 180px;
	}
	.lrow {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.lrow label {
		margin: 0;
	}
	.form-actions {
		padding-top: 4px;
	}
	.brow {
		display: flex;
		gap: 6px;
		min-width: 0;
	}
	.brow input {
		flex: 1;
		min-width: 0;
	}
	ul {
		margin: 4px 0 0;
		padding-left: 18px;
	}
	.note {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.srow {
		display: flex;
		gap: 6px;
	}
	.results {
		display: flex;
		flex-direction: column;
		gap: 1px;
		padding: 4px;
		max-height: 360px;
		overflow-y: auto;
	}
	.result {
		flex-direction: column;
		align-items: stretch;
		gap: 2px;
		padding: 7px 9px;
	}
	.rl1 {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.rtitle {
		font-size: var(--fs-ui);
		font-weight: 500;
	}
	.rl2 {
		font-size: var(--fs-meta);
		color: var(--text-muted);
	}
	.picked {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 10px 12px;
	}
	.prow {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	@media (max-width: 1000px) {
		.tf {
			grid-template-columns: minmax(0, 1fr);
		}
		.imp {
			position: static;
			order: -1;
		}
	}
</style>
