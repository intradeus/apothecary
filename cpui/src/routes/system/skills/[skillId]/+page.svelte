<script lang="ts">
	// One skill (TODO 38): its files on the left, the picked file in the
	// editor on the right. `new` creates one from a SKILL.md template. The
	// whole folder saves at once; the engine checks the frontmatter and cuts
	// a version in every skillset that lists the skill.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { untrack } from 'svelte';
	import { api } from '$lib/api';
	import type { Skill, SkillFileContent, Skillset } from '$lib/domain';
	import { ago, fmtAbs, fmtBytes } from '$lib/format';
	import { t, th } from '$lib/i18n/index.svelte';
	import { to } from '$lib/urls';
	import SkillFileEditor from '$lib/skills/SkillFileEditor.svelte';
	import { decodeText, encodeText, isText, SKILL_NAME } from '$lib/skills/files';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import FileChip from '$lib/ui/FileChip.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/ui/icons';

	const skillId = $derived(page.params.skillId ?? '');
	const creating = $derived(skillId === 'new');

	/** A file as the editor holds it: text when it reads as UTF-8, else the
	 *  bytes stay opaque. */
	type EditFile = { path: string; bytes: Uint8Array; mode?: number; text: string | null };

	let skill = $state<Skill | null>(null);
	let usedBy = $state<Skillset[]>([]);
	let name = $state('');
	let files = $state<EditFile[]>([]);
	let selected = $state('SKILL.md');
	let text = $state('');
	let loaded = $state(false);
	let loadErr = $state('');
	let error = $state('');
	let saving = $state(false);
	let refreshing = $state(false);
	let dirty = $state(false);
	let newPath = $state('');
	let renaming = $state<string | null>(null);
	let renameTo = $state('');
	let uploader = $state<HTMLInputElement | null>(null);

	const template = (n: string) => t('skills.skill.template', { name: n || 'my-skill' });
	let templateFor = 'my-skill';

	const toEdit = (f: SkillFileContent): EditFile => ({
		path: f.path,
		bytes: f.bytes,
		mode: f.mode,
		text: isText(f.bytes) ? decodeText(f.bytes) : null
	});

	async function load(id: string) {
		loaded = false;
		loadErr = '';
		dirty = false;
		if (id === 'new') {
			skill = null;
			name = '';
			templateFor = 'my-skill';
			files = [{ path: 'SKILL.md', bytes: new Uint8Array(), text: template('my-skill') }];
			selected = 'SKILL.md';
			usedBy = [];
			loaded = true;
			return;
		}
		try {
			const [d, sets] = await Promise.all([api.skills.get(id), api.skillsets.list()]);
			if (id !== skillId) return;
			skill = d.skill;
			name = d.skill.name;
			files = d.contents.map(toEdit);
			selected = files.some((f) => f.path === selected) ? selected : (files[0]?.path ?? 'SKILL.md');
			usedBy = sets.filter((s) => s.skillIds.includes(id));
		} catch (e) {
			loadErr = e instanceof Error ? e.message : String(e);
		} finally {
			if (id === skillId) loaded = true;
		}
	}
	$effect(() => {
		const id = skillId;
		untrack(() => void load(id));
	});

	// The picked file into the editor; the editor's text back into the file.
	$effect(() => {
		const p = selected;
		untrack(() => {
			text = files.find((f) => f.path === p)?.text ?? '';
		});
	});
	$effect(() => {
		const v = text;
		untrack(() => {
			const f = files.find((x) => x.path === selected);
			if (f && f.text !== null && f.text !== v) {
				f.text = v;
				dirty = true;
			}
		});
	});

	const current = $derived(files.find((f) => f.path === selected));
	const nameOk = $derived(SKILL_NAME.test(name));

	// While the SKILL.md is still the untouched template, the name typed
	// flows into it.
	function onName() {
		if (!creating) return;
		const md = files.find((f) => f.path === 'SKILL.md');
		if (md && md.text === template(templateFor)) {
			md.text = template(name);
			templateFor = name || 'my-skill';
			if (selected === 'SKILL.md') text = md.text;
		}
	}

	const cleanPath = (p: string) => p.trim().replace(/^\/+/, '').replace(/\/+/g, '/');
	const validPath = (p: string) =>
		p !== '' && !p.split('/').some((s) => s === '' || s === '.' || s === '..');

	function addFile() {
		const p = cleanPath(newPath);
		if (!validPath(p) || files.some((f) => f.path === p)) return;
		files.push({ path: p, bytes: new Uint8Array(), text: '' });
		newPath = '';
		selected = p;
		dirty = true;
	}
	function removeFile(p: string) {
		if (p === 'SKILL.md') return;
		files = files.filter((f) => f.path !== p);
		if (selected === p) selected = 'SKILL.md';
		dirty = true;
	}
	function startRename(p: string) {
		renaming = p;
		renameTo = p;
	}
	function commitRename() {
		const from = renaming;
		renaming = null;
		if (!from) return;
		const p = cleanPath(renameTo);
		if (p === from || !validPath(p) || files.some((f) => f.path === p)) return;
		const f = files.find((x) => x.path === from);
		if (!f) return;
		f.path = p;
		if (selected === from) selected = p;
		dirty = true;
	}
	async function upload(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		for (const file of input.files ?? []) {
			const bytes = new Uint8Array(await file.arrayBuffer());
			const p = file.name;
			const existing = files.find((f) => f.path === p);
			const next: EditFile = { path: p, bytes, text: isText(bytes) ? decodeText(bytes) : null };
			if (existing) Object.assign(existing, next);
			else files.push(next);
			selected = p;
			if (selected === p) text = next.text ?? '';
		}
		input.value = '';
		dirty = true;
	}

	async function save() {
		if (saving || !nameOk) return;
		saving = true;
		error = '';
		try {
			const draft = {
				name,
				files: files.map((f) => ({
					path: f.path,
					bytes: f.text !== null ? encodeText(f.text) : f.bytes,
					mode: f.mode
				}))
			};
			if (creating) {
				const created = await api.skills.create(draft);
				toast(t('skills.skill.created', { name: created.name }));
				await goto(to.skill(created.id));
			} else {
				skill = await api.skills.update(skillId, draft);
				dirty = false;
				toast(t('skills.skill.saved'));
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
			await api.skills.refresh(skillId);
			toast(t('skills.skill.refreshed'));
			await load(skillId);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			refreshing = false;
		}
	}
	async function del() {
		if (
			!(await confirmDialog({
				title: t('skills.skill.deleteTitle'),
				body: th('skills.skill.deleteBody'),
				action: t('skills.skill.deleteAction')
			}))
		)
			return;
		try {
			await api.skills.remove(skillId);
			toast(t('skills.skill.deleted'));
			await goto(to.skills());
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}
</script>

<a class="back small" href={to.skills()}>{I.back} {t('skills.crumb')}</a>

{#if loadErr}
	<ErrorCard cause={t('skills.skill.notFound')} detail={loadErr} onretry={() => load(skillId)} />
{:else if !loaded}
	<Skeleton pane lines={6} />
{:else}
	<div class="head">
		{#if creating}
			<div class="field grow">
				<label for="sk-name">{t('skills.skill.name')}</label>
				<input
					id="sk-name"
					class="mono"
					bind:value={name}
					oninput={onName}
					placeholder={t('skills.skill.namePh')}
					aria-invalid={name !== '' && !nameOk}
				/>
				<span class="help">{t('skills.skill.nameHelp')}</span>
			</div>
		{:else}
			<div class="title">
				<span class="display at mono">{name}</span>
				{#if skill?.source}<span class="pill sm t-brand">{t('skills.remote')}</span>{/if}
				{#if skill}<span class="desc">{skill.description}</span>{/if}
			</div>
		{/if}
		<div class="acts">
			{#if skill?.source}
				<button class="btn-sm" disabled={refreshing} aria-busy={refreshing} onclick={refresh}
					>{I.retry} {refreshing ? t('skills.skill.refreshing') : t('skills.skill.refresh')}</button
				>
			{/if}
			{#if !creating}
				<button class="btn-sm" onclick={del}>{t('skills.skill.delete')}</button>
			{/if}
			<button
				class="btn-primary"
				disabled={saving || !nameOk || (!dirty && !creating)}
				aria-busy={saving}
				onclick={save}>{creating ? t('skills.skill.create') : t('skills.skill.save')}</button
			>
		</div>
	</div>
	{#if error}<div class="err">{error}</div>{/if}

	<div class="split">
		<aside class="files pane">
			<div class="pane-head"><h2>{t('skills.skill.files')}</h2></div>
			<div class="flist">
				{#each files as f (f.path)}
					<div class="frow" class:sel={f.path === selected}>
						{#if renaming === f.path}
							<input
								class="mono"
								bind:value={renameTo}
								onkeydown={(e) => {
									if (e.key === 'Enter') commitRename();
									if (e.key === 'Escape') renaming = null;
								}}
								onblur={commitRename}
							/>
						{:else}
							<button class="fname mono ell" onclick={() => (selected = f.path)} title={f.path}
								>{f.path}</button
							>
							{#if f.text === null}<span class="fnt tiny">{fmtBytes(f.bytes.length)}</span>{/if}
							{#if f.path !== 'SKILL.md'}
								<button
									class="btn-icon btn-sm"
									title={t('skills.skill.rename')}
									aria-label={t('skills.skill.rename')}
									onclick={() => startRename(f.path)}>{I.more}</button
								>
								<button
									class="btn-icon btn-sm"
									title={t('skills.skill.removeFile', { path: f.path })}
									aria-label={t('skills.skill.removeFile', { path: f.path })}
									onclick={() => removeFile(f.path)}>{I.close}</button
								>
							{/if}
						{/if}
					</div>
				{/each}
			</div>
			<div class="fadd">
				<input
					class="mono"
					bind:value={newPath}
					placeholder={t('skills.skill.addFilePh')}
					aria-label={t('skills.skill.addFile')}
					onkeydown={(e) => e.key === 'Enter' && addFile()}
				/>
				<button class="btn-sm" onclick={addFile} disabled={!validPath(cleanPath(newPath))}
					>{I.add} {t('skills.skill.addFile')}</button
				>
				<button class="btn-sm" onclick={() => uploader?.click()}
					>{I.attach} {t('skills.skill.upload')}</button
				>
				<input bind:this={uploader} type="file" multiple hidden onchange={upload} />
			</div>
		</aside>

		<section class="editor pane">
			{#if current}
				<div class="pane-head"><h2 class="mono">{current.path}</h2></div>
				{#if current.text !== null}
					<div class="ed">
						<SkillFileEditor
							bind:value={text}
							path={current.path}
							label={t('skills.skill.editor', { path: current.path })}
						/>
					</div>
				{:else}
					<div class="bin">
						<FileChip name={current.path} size={current.bytes.length} />
						<span class="help">{t('skills.skill.binaryHelp')}</span>
					</div>
				{/if}
			{/if}
		</section>
	</div>

	{#if skill && (skill.source || usedBy.length)}
		<div class="meta">
			{#if skill.source}
				<div class="src">
					<span class="mut">{t('skills.skill.source')}</span>
					<a class="mono" href={skill.source.url} target="_blank" rel="noreferrer"
						>{skill.source.url} {I.external}</a
					>
					{#if skill.source.ref}<span class="fnt"
							>{t('skills.skill.sourceRef', { ref: skill.source.ref })}</span
						>{/if}
					{#if skill.source.commit}<span class="fnt mono"
							>{t('skills.skill.sourceCommit', { commit: skill.source.commit.slice(0, 10) })}</span
						>{/if}
					<span class="fnt" title={fmtAbs(skill.source.fetchedAt)}
						>{t('skills.skill.sourceFetched', { ago: ago(skill.source.fetchedAt) })}</span
					>
					<span class="help">{t('skills.skill.refreshHelp')}</span>
				</div>
			{/if}
			{#if usedBy.length}
				<div class="used">
					<span class="mut">{t('skills.skill.usedBy')}</span>
					{#each usedBy as s (s.id)}<a class="pill sm t-mut" href={to.skillset(s.id)}>{s.name}</a
						>{/each}
				</div>
			{/if}
		</div>
	{/if}
{/if}

<style>
	.back {
		align-self: flex-start;
		color: var(--mut);
	}
	.head {
		display: flex;
		align-items: flex-start;
		gap: 16px;
		flex-wrap: wrap;
	}
	.head .grow {
		flex: 1;
		min-width: 240px;
	}
	.title {
		flex: 1;
		min-width: 0;
		display: flex;
		align-items: baseline;
		gap: 10px;
		flex-wrap: wrap;
	}
	.title .at {
		font-size: var(--fs-section, 1.25rem);
	}
	.desc {
		color: var(--mut);
	}
	.acts {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-left: auto;
	}
	.split {
		display: flex;
		gap: 14px;
		min-height: 460px;
	}
	.files {
		width: 280px;
		flex-shrink: 0;
		display: flex;
		flex-direction: column;
	}
	.flist {
		display: flex;
		flex-direction: column;
		padding: 6px;
	}
	.frow {
		display: flex;
		align-items: center;
		gap: 4px;
		padding: 2px 4px;
		border-radius: var(--r-row);
	}
	.frow.sel {
		background: var(--p2);
	}
	.fname {
		flex: 1;
		min-width: 0;
		text-align: left;
		background: none;
		border: 0;
		padding: 4px 6px;
		color: var(--tx);
		cursor: pointer;
		font-size: var(--fs-label);
	}
	.frow input {
		flex: 1;
		min-width: 0;
	}
	.fadd {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		padding: 8px;
		margin-top: auto;
		border-top: 1px solid var(--ln);
	}
	.fadd input {
		flex: 1;
		min-width: 120px;
	}
	.editor {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.ed {
		flex: 1;
		min-height: 400px;
	}
	.bin {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 14px;
	}
	.meta {
		display: flex;
		flex-direction: column;
		gap: 8px;
		font-size: var(--fs-label);
	}
	.src,
	.used {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
	}
	@media (max-width: 900px) {
		.split {
			flex-direction: column;
		}
		.files {
			width: 100%;
		}
	}
</style>
