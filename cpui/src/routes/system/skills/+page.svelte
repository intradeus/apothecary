<script lang="ts">
	// The skill library (TODO 38): one's skills and the skillsets that group
	// them, plus the import form. Everything here is the person's own; a run
	// picks skillsets when it starts.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { Skill, SkillImportResult, Skillset } from '$lib/domain';
	import { ago } from '$lib/format';
	import { t } from '$lib/i18n/index.svelte';
	import { to } from '$lib/urls';
	import AddCard from '$lib/system/AddCard.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import HeaderAction from '$lib/ui/HeaderAction.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';

	let skills = $state<Skill[]>([]);
	let sets = $state<Skillset[]>([]);
	let loaded = $state(false);
	let error = $state('');

	let importing = $state(false);
	let url = $state('');
	let path = $state('');
	let busy = $state(false);
	let importErr = $state('');
	let result = $state<SkillImportResult | null>(null);

	const refresh = async () => {
		try {
			const [sk, ss] = await Promise.all([api.skills.list(), api.skillsets.list()]);
			skills = sk;
			sets = ss;
			loaded = true;
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	};
	onMount(refresh);

	const inSets = (id: string) => sets.filter((s) => s.skillIds.includes(id)).length;

	async function doImport() {
		if (!url.trim() || busy) return;
		busy = true;
		importErr = '';
		result = null;
		try {
			const r = await api.skills.import(url.trim(), path.trim() || undefined);
			result = r;
			toast(
				r.skillset
					? t('skills.importedSet', { name: r.skillset.name, count: r.skills.length })
					: t('skills.importedSkill', { name: r.skill?.name ?? '' })
			);
			url = '';
			path = '';
			await refresh();
		} catch (e) {
			importErr = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}
</script>

{#if error && !loaded}
	<ErrorCard cause={t('skills.loadError')} detail={error} onretry={refresh} />
{:else if !loaded}
	<section class="col">
		<SectionHeader title={t('skills.section')} />
		<Skeleton pane lines={3} />
	</section>
{:else}
	<HeaderAction>
		<div class="row nowrap">
			<button
				class:btn-sel={importing}
				aria-pressed={importing}
				onclick={() => (importing = !importing)}>{t('skills.import')}</button
			>
			<a class="btn" href={to.newSkillset()}>{I.add} {t('skills.newSet')}</a>
			<a class="btn btn-primary btn-lg" href={to.newSkill()}>{I.add} {t('skills.new')}</a>
		</div>
	</HeaderAction>

	{#if importing}
		<AddCard title={t('skills.importTitle')}>
			<div class="imp">
				<div class="field">
					<label for="imp-url">{t('skills.importUrl')}</label>
					<input
						id="imp-url"
						class="mono"
						bind:value={url}
						placeholder={t('skills.importUrlPh')}
						onkeydown={(e) => e.key === 'Enter' && doImport()}
					/>
					<span class="help">{t('skills.importUrlHelp')}</span>
				</div>
				<div class="field">
					<label for="imp-path">{t('skills.importPath')}</label>
					<input
						id="imp-path"
						class="mono"
						bind:value={path}
						placeholder={t('skills.importPathPh')}
					/>
					<span class="help">{t('skills.importPathHelp')}</span>
				</div>
				{#if importErr}<div class="err">{importErr}</div>{/if}
				<div class="row">
					<button
						class="btn-primary"
						disabled={busy || !url.trim()}
						aria-busy={busy}
						onclick={doImport}>{busy ? t('skills.importing') : t('skills.importGo')}</button
					>
					<button class="btn-ghost" onclick={() => (importing = false)}>{t('common.cancel')}</button
					>
				</div>
			</div>
		</AddCard>
	{/if}

	{#if result}
		<Callout hue="grn" glyph={I.ok} ondismiss={() => (result = null)}>
			{#if result.skillset}
				<a href={to.skillset(result.skillset.id)}
					>{t('skills.importedSet', {
						name: result.skillset.name,
						count: result.skills.length
					})}</a
				>
			{:else if result.skill}
				<a href={to.skill(result.skill.id)}
					>{t('skills.importedSkill', { name: result.skill.name })}</a
				>
			{/if}
			{#if result.warnings.length}
				<div class="warns">
					<span class="mut">{t('skills.importWarnings')}:</span>
					{#each result.warnings as w (w)}<span class="mono">{w}</span>{/each}
				</div>
			{/if}
		</Callout>
	{/if}

	<div class="sections">
		<section class="col">
			<SectionHeader
				title={t('skills.section')}
				count={skills.length}
				sub={t('skills.sectionSub')}
			/>
			{#if skills.length}
				<div class="pane rows">
					<div class="r head sk">
						<span>{t('skills.colSkill')}</span><span></span><span class="opt"
							>{t('skills.colFiles')}</span
						><span class="opt">{t('skills.colSets')}</span><span>{t('skills.colUpdated')}</span>
					</div>
					{#each skills as s (s.id)}
						<div class="r sk">
							<span class="main">
								<a class="name mono stretch" href={to.skill(s.id)}>{s.name}</a>
								<span class="desc ell">{s.description}</span>
							</span>
							<span>
								{#if s.source}<span class="pill sm t-brand">{t('skills.remote')}</span>{/if}
							</span>
							<span class="fnt opt">{s.files.length}</span>
							<span class="fnt opt">{inSets(s.id) || '—'}</span>
							<span class="fnt">{ago(s.updatedAt)}</span>
						</div>
					{/each}
				</div>
			{:else}
				<EmptyState compact title={t('skills.empty')} text={t('skills.emptySub')} />
			{/if}
		</section>

		<section class="col">
			<SectionHeader
				title={t('skills.setsSection')}
				count={sets.length}
				sub={t('skills.setsSectionSub')}
			/>
			{#if sets.length}
				<div class="pane rows">
					<div class="r head set">
						<span>{t('skills.colSet')}</span><span></span><span class="opt"
							>{t('skills.colVersion')}</span
						><span class="opt">{t('skills.colSkills')}</span><span>{t('skills.colUpdated')}</span>
					</div>
					{#each sets as s (s.id)}
						<div class="r set">
							<span class="main">
								<a class="name stretch" href={to.skillset(s.id)}>{s.name}</a>
								<span class="desc ell">{s.description}</span>
							</span>
							<span>
								{#if s.source}<span class="pill sm t-brand">{t('skills.remote')}</span>{/if}
							</span>
							<span class="opt"
								><span class="pill sm t-mut">{t('skills.version', { seq: s.headSeq })}</span></span
							>
							<span class="fnt opt">{s.skillIds.length}</span>
							<span class="fnt">{ago(s.updatedAt)}</span>
						</div>
					{/each}
				</div>
			{:else}
				<EmptyState compact title={t('skills.setsEmpty')} text={t('skills.setsEmptySub')} />
			{/if}
		</section>
	</div>
{/if}

<style>
	.imp {
		display: flex;
		flex-direction: column;
		gap: 12px;
		margin-top: 10px;
	}
	.imp .row {
		display: flex;
		gap: 8px;
	}
	.warns {
		display: flex;
		flex-wrap: wrap;
		gap: 6px 10px;
		margin-top: 6px;
		font-size: var(--fs-label);
	}
	.sk,
	.set {
		grid-template-columns: minmax(0, 1fr) auto 48px 64px 72px;
		font-size: var(--fs-label);
	}
	.main {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.name {
		color: var(--tx);
		font-weight: 600;
	}
	.name:hover {
		text-decoration: none;
	}
	.desc {
		color: var(--mut);
		font-size: var(--fs-label);
	}
	@media (max-width: 700px) {
		.sk,
		.set {
			grid-template-columns: minmax(0, 1fr) auto auto;
		}
		.opt {
			display: none;
		}
	}
</style>
