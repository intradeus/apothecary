<script lang="ts">
	// Workshop settings tab (DESIGN 7.9, edit): the setup form, and at its foot
	// the one way to delete the workshop — only once every run is archived.
	import { errorMessage } from '$lib/core/errors';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import { projectCan, type Flow, type Integration, type Project, type Run } from '$lib/domain';
	import { fleet } from '$lib/app/fleet.svelte';
	import { to } from '$lib/core/urls';
	import { t, th } from '$lib/i18n/index.svelte';
	import ProjectForm from './ProjectForm.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/design/icons';

	let {
		project,
		integrations,
		flows,
		runs,
		onsaved
	}: {
		project: Project;
		/** null while loading: the form snapshots them once, so it waits. */
		integrations: Integration[] | null;
		flows: Flow[] | null;
		runs: Run[];
		onsaved: (p: Project) => void;
	} = $props();

	// The form snapshots what it edits when it mounts: a new mount after a save
	// starts from what was saved, and Cancel discards the edits the same way.
	let mount = $state(0);

	async function saved(p: Project, envcheck: boolean) {
		onsaved(p);
		mount++;
		await fleet.refresh();
		if (envcheck) await goto(to.workshop(p.id, 'health'));
	}

	/** Runs that still hold the workshop: it can only be deleted once every
	 *  one of them is archived. */
	const blocking = $derived(runs.filter((r) => !r.archivedAt));

	let deleting = $state(false);
	let error = $state('');

	async function remove() {
		const ok = await confirmDialog({
			title: t('page.workshop.deleteTitle', { name: project.name }),
			body: th('page.workshop.deleteBody', { name: project.name }),
			action: t('page.workshop.deleteAction')
		});
		if (!ok) return;
		deleting = true;
		error = '';
		try {
			const gone = project.name;
			await api.projects.remove(project.id);
			await fleet.refresh();
			toast(t('page.workshop.deleted', { name: gone }));
			await goto(to.workshops());
		} catch (e) {
			error = errorMessage(e);
		} finally {
			deleting = false;
		}
	}
</script>

{#if !integrations || !flows}
	<Skeleton pane lines={6} />
{:else}
	{#key mount}
		<ProjectForm {integrations} {flows} existing={project} oncancel={() => mount++} onsaved={saved}>
			{#snippet after()}
				{#if projectCan(project, 'project:delete')}
					<section id="delete" class="glass pane">
						<div class="pane-head"><h2>{t('page.workshop.deleteHead')}</h2></div>
						<div class="pane-body del">
							{#if blocking.length}
								<p class="muted">
									<b>{t('page.workshop.archiveFirst')}.</b>
									{t('page.workshop.blockedText', { count: blocking.length })}
								</p>
							{:else}
								<p class="muted">{t('page.workshop.deleteLead')}</p>
							{/if}
							{#if error}<ErrorCard cause={t('page.workshop.deleteFailed')} detail={error} />{/if}
							<div class="acts">
								<button
									type="button"
									class="btn-danger"
									aria-busy={deleting}
									disabled={blocking.length > 0}
									onclick={remove}>{t('page.workshop.deleteAction')}</button
								>
								{#if blocking.length}
									<a class="btn" href={to.workshop(project.id, 'runs')}
										>{t('page.workshop.showRuns')} {I.forward}</a
									>
								{/if}
							</div>
						</div>
					</section>
				{/if}
			{/snippet}
		</ProjectForm>
	{/key}
{/if}

<style>
	section {
		scroll-margin-top: 8px;
	}
	.del {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 10px;
	}
	.del p {
		margin: 0;
		text-wrap: pretty;
	}
	.acts {
		display: flex;
		gap: 6px;
	}
</style>
