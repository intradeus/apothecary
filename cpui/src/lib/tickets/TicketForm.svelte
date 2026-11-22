<script lang="ts">
	// Ticket create/edit form, for a workshop on the internal tracker (a
	// remote tracker takes new issues itself). A ticket is a unit of work;
	// flow is NOT chosen here — it's picked per run when a run is started.
	import { onMount } from 'svelte';
	import { errorMessage } from '$lib/core/errors';
	import { renderBranch } from '$lib/core/branchname';
	import { t } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type {
		Attachment,
		ChangeType,
		Project,
		TicketDetail,
		TicketDraft,
		TrackerType
	} from '$lib/domain';
	import {
		CHANGE_TYPES,
		draftSubject,
		projectCan,
		ticketFill,
		type TicketFields
	} from '$lib/domain';
	import FillAssistant from '$lib/assistant/FillAssistant.svelte';
	import FillButton from '$lib/assistant/FillButton.svelte';
	import { FillGuard } from '$lib/assistant/fillGuard.svelte';
	import FileChips from '$lib/artifacts/FileChips.svelte';
	import AttachFiles from '$lib/artifacts/AttachFiles.svelte';
	import Select, { type SelectOption } from '$lib/ui/Select.svelte';
	import { toast } from '$lib/ui/toast.svelte';

	let {
		project,
		existing = null,
		onsaved,
		oncancel
	}: {
		/** The workshop the ticket is written through: its source holds it. */
		project: Project;
		existing?: TicketDetail | null;
		onsaved: (t: TicketDetail) => void;
		/** Show a Cancel button that calls this. */
		oncancel?: () => void;
	} = $props();

	/** Writing a ticket needs ticket:write in its workshop. */
	const canWrite = $derived(projectCan(project, 'ticket:write'));

	// svelte-ignore state_referenced_locally
	const init = $state.snapshot(existing) as TicketDetail | null;

	let title = $state(init?.title ?? '');
	let goal = $state(init?.body ?? '');
	let branch = $state(init?.baseBranch ?? '');
	let changeType = $state<ChangeType>(init?.changeType ?? 'feat');
	let typeId = $state(init?.typeId ?? '');
	let files = $state<File[]>([]);
	let types = $state<TrackerType[]>([]);

	// The local assistant: its fill sets the fields through the guard (undo,
	// the changed marker, the click before overwriting newer edits). A new
	// ticket's conversation is a draft's; each opening is a fresh session.
	let fillOpen = $state(false);
	const subject = init?.key ?? draftSubject();
	const guard = new FillGuard<TicketFields>(
		() => ({ title, goal, changeType, typeId }),
		(v) => {
			title = v.title;
			goal = v.goal;
			changeType = v.changeType as ChangeType;
			typeId = v.typeId;
		},
		ticketFill
	);
	const pageState = () => ({
		title,
		goal,
		change_type: changeType,
		type_id: typeId,
		base_branch: branch,
		issue_types: types.map((y) => ({ id: y.id, name: y.name, change_type: y.changeType }))
	});

	// Already-attached files, shown when editing so the list isn't invisible
	// state the form silently round-trips.
	let attached = $state<Attachment[]>([]);

	onMount(() => {
		// Through the workshop: its source is its owner's, which a
		// collaborator does not read.
		api.projects
			.ticketTypes(project.id)
			.then((l) => (types = l))
			.catch(() => {
				/* the type stays unset: the change type alone still names the work */
			});
		if (init)
			api.tickets
				.attachments(project.id, init.key)
				.then((a) => (attached = a.attachments))
				.catch(() => {});
	});

	const typeOptions = $derived<SelectOption[]>([
		{ value: '', label: t('form.ticket.noType') },
		...[...types]
			.sort((a, b) => a.position - b.position)
			.map((y) => ({ value: y.id, label: y.name }))
	]);
	const changeOptions: SelectOption<ChangeType>[] = CHANGE_TYPES.map((c) => ({
		value: c,
		label: c
	}));

	// A type picked brings the change type the source maps it to.
	function pickType(id: string) {
		const ty = types.find((y) => y.id === id);
		if (ty) changeType = ty.changeType;
	}

	let submitting = $state(false);
	let error = $state('');

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		if (submitting) return; // busy: Enter in a field must not send it twice
		submitting = true;
		error = '';
		try {
			let attachmentIds = init?.attachmentIds ?? [];
			if (files.length) {
				const uploaded = (await api.files.upload(files)).map((a) => a.id);
				attachmentIds = [...attachmentIds, ...uploaded];
			}
			// An empty base branch is named by the workshop's format when the
			// first run needs one.
			const draft: TicketDraft = {
				title: title.trim(),
				goal: goal.trim(),
				baseBranch: branch.trim(),
				changeType,
				typeId: typeId || undefined,
				attachmentIds,
				// An assistant's fill still on the form makes this its version.
				assisted: guard.changed.length > 0
			};
			const saved = init
				? await api.tickets.update(project.id, init.key, draft)
				: await api.tickets.create(project.id, draft);
			guard.settle();
			toast(init ? t('form.ticket.saved') : t('form.ticket.created'));
			onsaved(saved);
		} catch (err) {
			error = errorMessage(err);
		} finally {
			submitting = false;
		}
	}
</script>

<form class="tf glass pane" onsubmit={submit}>
	<div class="field">
		<label for="t-title">{t('form.ticket.title')}</label>
		<input
			id="t-title"
			bind:value={title}
			required
			placeholder={t('form.ticket.titlePlaceholder')}
		/>
	</div>

	<div class="field">
		<label for="t-goal">{t('form.ticket.goal')}</label>
		<span class="help">{t('form.ticket.goalHelp')}</span>
		<textarea
			id="t-goal"
			class="goal"
			bind:value={goal}
			required
			rows="10"
			placeholder={t('form.ticket.goalPlaceholder')}></textarea>
	</div>

	<div class="trow">
		<div class="field">
			<label for="t-type">{t('form.ticket.type')}</label>
			<Select id="t-type" options={typeOptions} bind:value={typeId} onchange={pickType} />
		</div>
		<div class="field">
			<label for="t-change">{t('form.ticket.changeType')}</label>
			<Select
				id="t-change"
				options={changeOptions}
				bind:value={changeType}
				mono
				title={t('form.ticket.changeTypeTitle')}
			/>
		</div>
	</div>

	<div class="field">
		<label for="t-branch"
			>{t('form.ticket.baseBranch')}
			<span class="muted">· {t('form.ticket.optional')}</span></label
		>
		<input
			id="t-branch"
			bind:value={branch}
			placeholder={renderBranch(
				project.repo.branchFormat,
				changeType,
				'',
				title || 'Short description'
			)}
			class="mono"
		/>
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
	<div class="form-actions">
		<span class="lead"><FillButton {guard} onclick={() => (fillOpen = true)} /></span>
		{#if oncancel}<button type="button" class="btn-ghost" onclick={oncancel}
				>{t('common.cancel')}</button
			>{/if}
		<button
			class="btn-primary btn-lg"
			type="submit"
			aria-busy={submitting}
			disabled={!canWrite || !title.trim() || !goal.trim()}
			title={canWrite ? undefined : t('common.needsPermission')}
		>
			{init ? t('form.ticket.save') : t('form.ticket.create')}
		</button>
	</div>
</form>
<FillAssistant
	bind:open={fillOpen}
	kind="ticket"
	{subject}
	workshop={project.id}
	{guard}
	{pageState}
/>

<style>
	.lead {
		margin-right: auto;
	}
	.tf {
		padding: 16px 18px 18px;
		display: flex;
		flex-direction: column;
		gap: 14px;
		min-width: 0;
	}
	.goal {
		min-height: 180px;
	}
	.trow {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		gap: 12px;
	}
	.form-actions {
		padding-top: 4px;
	}
	@media (max-width: 760px) {
		.trow {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
