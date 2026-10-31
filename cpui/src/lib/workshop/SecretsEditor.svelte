<script lang="ts">
	// The workshop's secrets: names and when each was last set — a value is
	// written, never read back. Each change is saved the moment it is made
	// (this sits inside the setup form, but its Save does not carry them),
	// so Enter here sets the secret rather than submitting the form.
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { t, th } from '$lib/i18n/index.svelte';
	import { api } from '$lib/api';
	import type { ProjectSecret } from '$lib/domain';
	import { ago, fmtAbs } from '$lib/core/format';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Loading from '$lib/ui/Loading.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { I } from '$lib/design/icons';

	let { projectId }: { projectId: string } = $props();

	const SLUG = /^[a-z][a-z0-9_-]*$/;

	let secrets = $state<ProjectSecret[] | null>(null);
	let loadError = $state('');
	let error = $state('');
	let busy = $state('');

	// The add row.
	let name = $state('');
	let value = $state('');
	// The secret whose value is being replaced, and the new value.
	let replacing = $state('');
	let replacement = $state('');

	async function load() {
		loadError = '';
		try {
			secrets = await api.projects.secrets(projectId);
		} catch (e) {
			loadError = errorMessage(e);
		}
	}
	onMount(load);

	const nameBad = $derived(!!name.trim() && !SLUG.test(name.trim()));
	const nameTaken = $derived(!!secrets?.some((s) => s.name === name.trim()));

	async function set(n: string, v: string, replace: boolean) {
		if (!n || !v || busy) return;
		busy = n;
		error = '';
		try {
			const saved = await api.projects.setSecret(projectId, n, v);
			secrets = [...(secrets ?? []).filter((s) => s.name !== saved.name), saved].sort((a, b) =>
				a.name.localeCompare(b.name)
			);
			toast(t(replace ? 'form.secrets.toastReplaced' : 'form.secrets.toastSet', { name: n }));
			if (replace) {
				replacing = '';
				replacement = '';
			} else {
				name = '';
				value = '';
			}
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = '';
		}
	}

	async function remove(n: string) {
		const ok = await confirmDialog({
			title: t('form.secrets.confirmTitle', { name: n }),
			body: th('form.secrets.confirmBody', { name: n }),
			action: t('form.secrets.delete'),
			tone: 'danger'
		});
		if (!ok) return;
		busy = n;
		error = '';
		try {
			await api.projects.deleteSecret(projectId, n);
			secrets = (secrets ?? []).filter((s) => s.name !== n);
			toast(t('form.secrets.toastDeleted', { name: n }), { tone: 'info' });
		} catch (e) {
			error = errorMessage(e);
		} finally {
			busy = '';
		}
	}

	/** Enter sets, never submits the setup form around it. */
	const onEnter = (fn: () => void) => (e: KeyboardEvent) => {
		if (e.key !== 'Enter') return;
		e.preventDefault();
		fn();
	};
	const addOne = () => !nameBad && !nameTaken && set(name.trim(), value, false);
</script>

<div class="sec">
	{#if loadError}
		<ErrorCard cause={t('form.secrets.loadFail')} detail={loadError} onretry={load} />
	{:else if secrets === null}
		<Loading />
	{:else}
		{#if secrets.length}
			<div class="list card-in">
				{#each secrets as s (s.name)}
					<div class="r">
						<span class="mono nm">{s.name}</span>
						{#if replacing === s.name}
							<input
								class="mono"
								type="password"
								autocomplete="new-password"
								bind:value={replacement}
								placeholder={t('form.secrets.newValue')}
								aria-label={t('form.secrets.newValueFor', { name: s.name })}
								onkeydown={onEnter(() => set(s.name, replacement, true))}
							/>
							<button
								type="button"
								class="btn-sm btn-sel"
								disabled={!replacement || !!busy}
								aria-busy={busy === s.name}
								onclick={() => set(s.name, replacement, true)}>{t('form.secrets.save')}</button
							>
							<button
								type="button"
								class="btn-sm btn-ghost"
								onclick={() => ((replacing = ''), (replacement = ''))}>{t('common.cancel')}</button
							>
						{:else}
							<span class="faint when" title={fmtAbs(s.updatedAt)}
								>{t('form.secrets.setAgo', { ago: ago(s.updatedAt) })}</span
							>
							<span class="spacer"></span>
							<button
								type="button"
								class="btn-sm"
								disabled={!!busy}
								onclick={() => ((replacing = s.name), (replacement = ''))}
								>{t('form.secrets.replace')}</button
							>
							<button
								type="button"
								class="btn-icon"
								disabled={!!busy}
								aria-busy={busy === s.name}
								aria-label={t('form.secrets.deleteNamed', { name: s.name })}
								onclick={() => remove(s.name)}>{I.close}</button
							>
						{/if}
					</div>
				{/each}
			</div>
		{:else}
			<div class="note">{t('form.secrets.none')}</div>
		{/if}

		<div class="add">
			<input
				class="mono"
				class:invalid={nameBad || nameTaken}
				bind:value={name}
				placeholder="github_token"
				aria-label={t('form.secrets.name')}
				autocomplete="off"
				spellcheck="false"
				onkeydown={onEnter(addOne)}
			/>
			<input
				class="mono"
				type="password"
				autocomplete="new-password"
				bind:value
				placeholder={t('form.secrets.value')}
				aria-label={t('form.secrets.value')}
				onkeydown={onEnter(addOne)}
			/>
			<button
				type="button"
				class="btn-sm"
				disabled={!name.trim() || !value || nameBad || nameTaken || !!busy}
				aria-busy={!!busy && busy === name.trim()}
				onclick={addOne}>{I.add} {t('form.secrets.add')}</button
			>
		</div>
		{#if nameBad}
			<div class="error">{t('form.secrets.badName')}</div>
		{:else if nameTaken}
			<div class="error">{t('form.secrets.taken', { name: name.trim() })}</div>
		{/if}
		{#if error}<ErrorCard
				cause={t('form.secrets.saveFail')}
				detail={error}
				ondismiss={() => (error = '')}
			/>{/if}
	{/if}
</div>

<style>
	.sec {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin-top: 4px;
		max-width: 620px;
	}
	.list {
		overflow: hidden;
	}
	.r {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 10px;
		border-top: 1px solid var(--line);
		min-width: 0;
	}
	.r:first-child {
		border-top: 0;
	}
	.nm {
		font-size: var(--fs-label);
		font-weight: 600;
	}
	.r input {
		flex: 1;
		min-width: 0;
	}
	.when {
		font-size: var(--fs-meta);
	}
	.add {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr) auto;
		gap: 6px;
		align-items: center;
	}
	@media (max-width: 760px) {
		.add {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
