<script lang="ts">
	import { to } from '$lib/guild';
	import { lore } from '$lib/lore';
	// Ticket create/edit form. A ticket is a unit of work; flow is NOT chosen
	// here — it's picked per run when a run is started.
	//
	// A new ticket can also be COPIED from a tracker (Jira): pick a source,
	// search it, and the chosen issue prefills this form. The copy is taken on
	// submit — title/goal are whatever the fields say by then, and the issue's
	// attachments come across as ticket attachments.
	import { api } from '$lib/api';
	import { fmtBytes } from '$lib/format';
	import type { Attachment, Integration, Project, SourceTicket, Ticket } from '$lib/domain';
	import FileChips from '$lib/FileChips.svelte';
	import AttachFiles from '$lib/AttachFiles.svelte';
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
			sources = (await api.integrations.list()).filter((i) => i.port === 'ticketsource');
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
			searchErr = e instanceof Error ? e.message : String(e);
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
	async function pick(t: SourceTicket) {
		searchErr = '';
		try {
			picked = await api.integrations.sourceTicket(sourceId, t.key);
		} catch (e) {
			searchErr = e instanceof Error ? e.message : String(e);
			return;
		}
		title = picked.title ?? '';
		goal = picked.body ?? '';
		if (!branchEdited) branch = `${picked.key.toLowerCase()}-${slugify(picked.title ?? '')}`;
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

	// Prefill the branch from a slug of the title until the user edits it.
	let branchEdited = $state(init != null);
	// The WHOLE title: a git branch has no meaningful length limit, and the one
	// thing that does — the run's container name — is derived and truncated
	// separately by the engine (engine.workspaceName), so nothing is gained by
	// cutting the readable name short here.
	const slugify = (s: string) =>
		s
			.toLowerCase()
			.replace(/[^a-z0-9]+/g, '-')
			.replace(/^-+|-+$/g, '');
	$effect(() => {
		if (!branchEdited && !picked) branch = slugify(title);
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
				attachmentIds,
				...(picked ? { sourceIntegrationId: sourceId, sourceKey: picked.key } : {})
			};
			const t = init ? await api.tickets.update(init.id, body) : await api.tickets.create(body);
			if (t.warnings?.length) {
				warnings = t.warnings;
				created = t;
				return;
			}
			onsaved(t);
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			submitting = false;
		}
	}
</script>

<form class="tf" class:edit={!!init} onsubmit={submit}>
	<section class="main c">
		{#if projects.length === 0}
			<div class="warn">
				No {lore('workshops').toLowerCase()} yet — set up
				<a href={to.system('integrations')}>integrations</a> and create a
				<a href={to.newWorkshop()}>{lore('workshop').toLowerCase()}</a> first.
			</div>
		{/if}

		<div class="field">
			<label for="t-project">{lore('workshop')}</label>
			<select id="t-project" bind:value={projectId} required disabled={init != null}>
				{#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
			</select>
			{#if init}<span class="help"
					>Locked: a ticket stays in the {lore('workshop').toLowerCase()} it was written for.</span
				>{/if}
		</div>

		<div class="field">
			<label for="t-title">Title</label>
			<input id="t-title" bind:value={title} required placeholder="Add a Farewell function" />
		</div>

		<div class="field">
			<label for="t-goal">Goal</label>
			<span class="help"
				>What should the factory build, and what does done look like? Markdown.</span
			>
			<textarea
				id="t-goal"
				class="goal"
				bind:value={goal}
				required
				rows="10"
				placeholder="Add a GET /farewell endpoint that returns “goodbye”. Done = the handler, a test, and the route wired up."
			></textarea>
		</div>

		<div class="field">
			<div class="lrow">
				<label for="t-branch">Base branch</label>
				{#if branchEdited && !init}
					<span class="pill t-amb">edited</span>
					<button
						type="button"
						class="btn-ghost btn-sm"
						title="Go back to the branch derived from the title"
						onclick={() => {
							branchEdited = false;
							branch = picked
								? `${picked.key.toLowerCase()}-${slugify(picked.title ?? '')}`
								: slugify(title);
						}}>↺ auto</button
					>
				{:else if !init}
					<span class="pill t-mut">auto</span>
				{/if}
			</div>
			<input
				id="t-branch"
				bind:value={branch}
				oninput={() => (branchEdited = true)}
				placeholder="feat/PROJ-123-short-description"
				class="mono"
			/>
			<span class="help">Each run appends a short unique suffix, so runs never share a branch.</span
			>
		</div>

		<div class="field">
			<span class="flabel">Attachments</span>
			{#if attached.length}
				<FileChips
					items={attached.map((a) => ({ ...a, kind: 'attachment' as const }))}
					label="Already attached — delivered into every run's workspace"
				/>
			{/if}
			<AttachFiles bind:files label="Drop documents or images for the agent, or browse" />
		</div>

		{#if error}<div class="err">{error}</div>{/if}
		{#if warnings.length && created}
			<div class="warn">
				<b>Ticket created, but some attachments did not come across:</b>
				<ul>
					{#each warnings as w (w)}<li>{w}</li>{/each}
				</ul>
			</div>
			<div class="foot">
				<button class="btn-primary" type="button" onclick={() => onsaved(created!)}
					>Continue to the ticket</button
				>
			</div>
		{:else}
			<div class="foot">
				{#if oncancel}<button type="button" class="btn-ghost" onclick={oncancel}>Cancel</button
					>{/if}
				<button
					class="btn-primary btn-lg"
					type="submit"
					disabled={submitting || !title.trim() || !goal.trim() || !projectId}
				>
					{submitting
						? 'Saving…'
						: init
							? 'Save ticket'
							: picked
								? `Create from ${picked.key}`
								: 'Create ticket'}
				</button>
			</div>
		{/if}
	</section>

	{#if !init}
		<aside class="imp c">
			<div class="ih">
				<h2>Import from tracker</h2>
				<span class="help"
					>Copy an issue in: title, goal and attachments. Nothing is written back.</span
				>
			</div>
			{#if sources.length === 0}
				<div class="none">
					No ticket source connected.
					<a href={to.system('integrations')}>Add a tracker integration →</a>
				</div>
			{:else if picked}
				<div class="picked">
					<div class="prow">
						<span class="tag">{picked.key}</span>
						<span class="ell grow">{picked.title}</span>
					</div>
					<div class="help">from {sourceName(sourceId)}</div>
					<!-- Name every file that will be copied — and say so plainly when the
					     issue has none, so "where are my attachments?" is answered before
					     the ticket is created rather than after. -->
					{#if picked.attachments?.length}
						<div class="help">
							{picked.attachments.length} file{picked.attachments.length > 1 ? 's' : ''} will be copied
							into the ticket:
						</div>
						<div class="chips">
							{#each picked.attachments as a (a.filename)}
								<span class="fchip" title={a.mime ?? ''}>
									<span class="mut">▤</span>
									<span class="ell">{a.filename}</span>
									{#if a.size}<span class="mono mut">{fmtBytes(a.size)}</span>{/if}
								</span>
							{/each}
						</div>
					{:else}
						<div class="help">This issue has no attachments.</div>
					{/if}
					<div class="prow">
						{#if picked.url}
							<a class="btn btn-sm" href={picked.url} target="_blank" rel="noreferrer"
								>Open in tracker ↗</a
							>
						{/if}
						<button type="button" class="btn-ghost btn-sm" onclick={clearImport}>Clear</button>
					</div>
				</div>
			{:else}
				<div class="field">
					<label for="t-src">Tracker</label>
					<select
						id="t-src"
						value={sourceId}
						onchange={(e) => {
							const v = (e.target as HTMLSelectElement).value;
							if (v) openSource(v);
							else clearImport();
						}}
					>
						<option value="">Choose a tracker…</option>
						{#each sources as s (s.id)}<option value={s.id}>{s.name} ({s.kind})</option>{/each}
					</select>
				</div>
				{#if sourceId}
					<div class="srow">
						<input
							type="search"
							bind:value={query}
							placeholder="2624, PROJ-2624, words, or jql:…"
							onkeydown={(e) => {
								if (e.key === 'Enter') {
									e.preventDefault();
									search();
								}
							}}
						/>
						<button type="button" onclick={search} disabled={searching}>
							{searching ? 'Searching…' : 'Search'}
						</button>
					</div>
					{#if searchErr}<div class="err">{searchErr}</div>{/if}
					{#if searching && !results.length}
						<div class="help">Searching…</div>
					{:else if results.length}
						<div class="results">
							{#each results as r (r.key)}
								<button type="button" class="result" onclick={() => pick(r)}>
									<span class="rl1">
										<span class="tag">{r.key}</span>
										<span class="ell rtitle">{r.title}</span>
									</span>
									<span class="rl2 ell">
										{[r.type, r.status, r.assignee].filter(Boolean).join(' · ')}
									</span>
								</button>
							{/each}
						</div>
					{:else if !searchErr}
						<div class="help">No matching issues.</div>
					{/if}
				{/if}
			{/if}
		</aside>
	{/if}
</form>

<style>
	.tf {
		display: grid;
		grid-template-columns: minmax(0, 1fr) 360px;
		gap: 12px;
		align-items: start;
	}
	.tf.edit {
		grid-template-columns: minmax(0, 1fr);
	}
	.main,
	.imp {
		border-radius: 16px;
		padding: 16px 18px 18px;
		display: flex;
		flex-direction: column;
		gap: 14px;
		min-width: 0;
	}
	.imp {
		gap: 12px;
		position: sticky;
		top: 0;
	}
	.ih {
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.ih h2 {
		font-size: 19px;
	}
	.flabel {
		font-size: 12px;
		font-weight: 500;
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
	.lrow .pill {
		font-size: 10.5px;
		padding: 0 7px;
	}
	.foot {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		padding-top: 4px;
	}
	.warn {
		font-size: 12px;
		color: var(--amb);
		background: var(--ambBg);
		border: 1px solid var(--ambLn);
		border-radius: 12px;
		padding: 8px 12px;
	}
	.warn ul {
		margin: 4px 0 0;
		padding-left: 18px;
	}
	.none {
		font-size: 12px;
		color: var(--mut);
		border: 1px dashed var(--ln2);
		border-radius: 12px;
		padding: 12px;
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
		max-height: 360px;
		overflow-y: auto;
		border: 1px solid var(--ln);
		border-radius: 12px;
	}
	.result {
		all: unset;
		box-sizing: border-box;
		cursor: pointer;
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 8px 10px;
		border-top: 1px solid var(--ln);
		min-width: 0;
	}
	.result:first-child {
		border-top: 0;
	}
	.result:hover {
		background: var(--p3);
	}
	.result:focus-visible {
		outline: 2px solid var(--brand);
		outline-offset: -2px;
	}
	.rl1 {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.rtitle {
		font-size: 12.5px;
		font-weight: 500;
	}
	.rl2 {
		font-size: 11px;
		color: var(--mut);
	}
	.picked {
		display: flex;
		flex-direction: column;
		gap: 8px;
		background: var(--p2);
		border: 1px solid var(--ln);
		border-radius: 13px;
		padding: 10px 12px;
	}
	.prow {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.chips {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.fchip {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		min-width: 0;
	}
	.fchip .mono {
		font-size: 10.5px;
		flex: none;
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
