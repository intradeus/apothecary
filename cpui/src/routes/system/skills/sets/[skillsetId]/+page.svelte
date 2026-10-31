<script lang="ts">
	// One skillset (TODO 38): name, description and which skills, with its
	// versions beside — every saved change of content cuts one, and a run
	// pins one. `new` creates (and cuts v1).
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { Skill, Skillset, SkillsetVersion } from '$lib/domain';
	import { ago, fmtAbs } from '$lib/format';
	import { t, th } from '$lib/i18n/index.svelte';
	import { to } from '$lib/urls';
	import CheckboxGroup from '$lib/ui/CheckboxGroup.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';

	const setId = $derived(page.params.skillsetId ?? '');
	const creating = $derived(setId === 'new');

	let set = $state<Skillset | null>(null);
	let skills = $state<Skill[]>([]);
	let versions = $state<SkillsetVersion[]>([]);
	let name = $state('');
	let description = $state('');
	let picked = $state<string[]>([]);
	let loaded = $state(false);
	let loadErr = $state('');
	let error = $state('');
	let saving = $state(false);
	let refreshing = $state(false);
	let openVersion = $state<number | null>(null);

	async function load(id: string) {
		loaded = false;
		loadErr = '';
		try {
			skills = await api.skills.list();
			if (id === 'new') {
				set = null;
				name = '';
				description = '';
				picked = [];
				versions = [];
			} else {
				const [s, vs] = await Promise.all([api.skillsets.get(id), api.skillsets.versions(id)]);
				if (id !== setId) return;
				set = s;
				name = s.name;
				description = s.description;
				picked = s.skillIds;
				versions = vs;
			}
		} catch (e) {
			loadErr = e instanceof Error ? e.message : String(e);
		} finally {
			if (id === setId) loaded = true;
		}
	}
	$effect(() => {
		const id = setId;
		untrack(() => void load(id));
	});

	const options = $derived(
		skills.map((s) => ({ value: s.id, label: s.name, detail: s.description }))
	);
	const dirty = $derived(
		creating ||
			!set ||
			name.trim() !== set.name ||
			description.trim() !== set.description ||
			picked.join(',') !== set.skillIds.join(',')
	);
	const nameById = $derived(new Map(skills.map((s) => [s.id, s.name])));

	async function save() {
		if (saving || !name.trim()) return;
		saving = true;
		error = '';
		try {
			const draft = { name: name.trim(), description: description.trim(), skillIds: picked };
			if (creating) {
				const created = await api.skillsets.create(draft);
				toast(t('skills.set.created', { name: created.name }));
				await goto(to.skillset(created.id));
			} else {
				set = await api.skillsets.update(setId, draft);
				versions = await api.skillsets.versions(setId);
				toast(t('skills.set.saved'));
			}
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			saving = false;
		}
	}
	async function refresh() {
		if (refreshing) return;
		refreshing = true;
		error = '';
		try {
			const before = set?.headSeq ?? 0;
			const r = await api.skillsets.refresh(setId);
			toast(
				r.skillset.headSeq === before
					? t('skills.set.refreshedNoChange')
					: t('skills.set.refreshed')
			);
			if (r.warnings.length)
				error = t('skills.set.refreshWarnings', { list: r.warnings.join('; ') });
			await load(setId);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			refreshing = false;
		}
	}
	async function del() {
		if (
			!(await confirmDialog({
				title: t('skills.set.deleteTitle'),
				body: th('skills.set.deleteBody'),
				action: t('skills.set.deleteAction')
			}))
		)
			return;
		try {
			await api.skillsets.remove(setId);
			toast(t('skills.set.deleted'));
			await goto(to.skills());
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}
</script>

<a class="back small" href={to.skills()}>{I.back} {t('skills.crumb')}</a>

{#if loadErr}
	<ErrorCard cause={t('skills.set.notFound')} detail={loadErr} onretry={() => load(setId)} />
{:else if !loaded}
	<Skeleton pane lines={6} />
{:else}
	<div class="split">
		<section class="form pane">
			<div class="pane-head">
				<h2>{creating ? t('skills.set.crumbNew') : set?.name}</h2>
				{#if set}<span class="pill sm t-mut">{t('skills.version', { seq: set.headSeq })}</span>{/if}
				{#if set?.source}<span class="pill sm t-brand">{t('skills.remote')}</span>{/if}
			</div>
			<div class="body">
				<div class="field">
					<label for="ss-name">{t('skills.set.name')}</label>
					<input id="ss-name" bind:value={name} placeholder={t('skills.set.namePh')} />
				</div>
				<div class="field">
					<label for="ss-desc">{t('skills.set.description')}</label>
					<textarea
						id="ss-desc"
						rows="2"
						bind:value={description}
						placeholder={t('skills.set.descriptionPh')}></textarea>
				</div>
				<div class="field">
					<span class="flabel">{t('skills.set.members')}</span>
					{#if options.length}
						<CheckboxGroup {options} bind:value={picked} label={t('skills.set.members')} />
						<span class="help">{t('skills.set.membersHelp')}</span>
					{:else}
						<span class="help"
							>{t('skills.set.noSkills')} <a href={to.newSkill()}>{t('skills.new')}</a></span
						>
					{/if}
				</div>
				{#if set?.source}
					<div class="src">
						<span class="mut">{t('skills.set.source')}</span>
						<a class="mono" href={set.source.url} target="_blank" rel="noreferrer"
							>{set.source.url} {I.external}</a
						>
						{#if set.source.commit}<span class="fnt mono"
								>{t('skills.skill.sourceCommit', { commit: set.source.commit.slice(0, 10) })}</span
							>{/if}
						<span class="fnt" title={fmtAbs(set.source.fetchedAt)}
							>{t('skills.skill.sourceFetched', { ago: ago(set.source.fetchedAt) })}</span
						>
					</div>
				{/if}
				{#if error}<div class="err">{error}</div>{/if}
				<div class="acts">
					{#if set?.source}
						<button class="btn-sm" disabled={refreshing} aria-busy={refreshing} onclick={refresh}
							>{I.retry} {refreshing ? t('skills.set.refreshing') : t('skills.set.refresh')}</button
						>
					{/if}
					{#if !creating}<button class="btn-sm" onclick={del}>{t('skills.set.delete')}</button>{/if}
					<span class="spacer"></span>
					<button
						class="btn-primary"
						disabled={saving || !dirty || !name.trim()}
						aria-busy={saving}
						onclick={save}>{creating ? t('skills.set.create') : t('skills.set.save')}</button
					>
				</div>
			</div>
		</section>

		{#if !creating}
			<aside class="vers pane">
				<div class="pane-head">
					<h2>{t('skills.set.versions')}</h2>
					<span class="mono fnt">{versions.length}</span>
				</div>
				<p class="help sp">{t('skills.set.versionsHelp')}</p>
				<div class="vlist">
					{#each versions as v (v.seq)}
						<div class="vr" class:cur={v.seq === set?.headSeq}>
							<button
								class="vhead"
								aria-expanded={openVersion === v.seq}
								onclick={() => (openVersion = openVersion === v.seq ? null : v.seq)}
							>
								<span class="vn">v{v.seq}</span>
								<span class="mono fnt">@{v.hash.slice(0, 10)}</span>
								{#if v.seq === set?.headSeq}<span class="pill sm t-grn"
										>{t('skills.set.current')}</span
									>{/if}
								<span class="spacer"></span>
								<span class="fnt tiny" title={fmtAbs(v.createdAt)}>{ago(v.createdAt)}</span>
								<span class="fnt">{openVersion === v.seq ? I.menu : I.caret}</span>
							</button>
							{#if openVersion === v.seq}
								<ul class="members">
									{#each v.members as m (m.skillId)}
										<li>
											<span class="mono">{m.name}</span>
											<span class="fnt mono">@{m.hash.slice(0, 8)}</span>
											{#if nameById.has(m.skillId)}<a class="small" href={to.skill(m.skillId)}
													>{I.forward}</a
												>{/if}
										</li>
									{:else}
										<li class="fnt">{t('ui.steplist.none')}</li>
									{/each}
								</ul>
							{/if}
						</div>
					{:else}
						<div class="fnt small sp">{t('skills.set.noVersions')}</div>
					{/each}
				</div>
			</aside>
		{/if}
	</div>
{/if}

<style>
	.back {
		align-self: flex-start;
		color: var(--mut);
	}
	.split {
		display: flex;
		gap: 14px;
		align-items: flex-start;
	}
	.form {
		flex: 1;
		min-width: 0;
	}
	.pane-head {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: 14px;
		padding: 14px;
	}
	.src {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
		font-size: var(--fs-label);
	}
	.acts {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.vers {
		width: 340px;
		flex-shrink: 0;
	}
	.sp {
		padding: 0 14px;
	}
	.vlist {
		display: flex;
		flex-direction: column;
		padding: 0 8px 10px;
	}
	.vr {
		border-top: 1px solid var(--ln);
		font-size: var(--fs-label);
	}
	.vr.cur {
		background: var(--p2);
		border-radius: var(--r-row);
	}
	.vhead {
		width: 100%;
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 7px 8px;
		background: none;
		border: 0;
		color: inherit;
		cursor: pointer;
		text-align: left;
	}
	.vn {
		font-weight: 600;
		color: var(--tx);
	}
	.members {
		list-style: none;
		margin: 0;
		padding: 0 8px 8px 28px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.members li {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	@media (max-width: 900px) {
		.split {
			flex-direction: column;
			align-items: stretch;
		}
		.vers {
			width: 100%;
		}
	}
</style>
