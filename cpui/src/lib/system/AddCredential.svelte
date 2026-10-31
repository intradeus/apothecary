<script lang="ts">
	// Adding a model provider credential, three ways
	// (docs/model-providers-plan.md):
	//   - an API key for a provider that takes one;
	//   - signing in from the engine: the provider's own page, then either a
	//     code typed there (device sign-in, polled here until approved) or a
	//     code / address pasted back here — the tokens go to the engine;
	//   - importing a file a harness CLI wrote.
	import { errorMessage } from '$lib/core/errors';
	import { onDestroy } from 'svelte';
	import { api } from '$lib/api';
	import type { Credential, ProviderInfo, SignInPending } from '$lib/domain';
	import Select from '$lib/ui/Select.svelte';
	import Callout from '$lib/ui/Callout.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { t } from '$lib/i18n/index.svelte';

	let {
		mode,
		providers,
		onadded,
		oncancel
	}: {
		mode: 'key' | 'signin' | 'import';
		providers: ProviderInfo[];
		/** done: close the panel (an import with refusals stays open to show them). */
		onadded: (created: Credential[], done: boolean) => void;
		oncancel: () => void;
	} = $props();

	const keyable = $derived(providers.filter((p) => p.key));
	const signable = $derived(providers.filter((p) => p.signIn));
	const nameOf = (id: string) => providers.find((p) => p.id === id)?.name ?? id;

	let provider = $state('');
	let key = $state('');
	let name = $state('');
	let text = $state('');
	let pasted = $state('');
	let busy = $state(false);
	let error = $state('');
	let rejected = $state<string[]>([]);
	let pending = $state<SignInPending | null>(null);
	let timer: ReturnType<typeof setTimeout> | undefined;
	let closed = false;

	onDestroy(() => {
		closed = true;
		clearTimeout(timer);
	});

	async function saveKey(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const c = await api.credentials.addKey(provider, key.trim(), name.trim() || undefined);
			toast(t('providers.added', { name: c.name }));
			onadded([c], true);
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function importFile(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		rejected = [];
		try {
			const r = await api.credentials.importFile(text);
			rejected = r.rejected;
			if (r.created.length) {
				toast(t('providers.imported', { count: r.created.length }));
				onadded(r.created, r.rejected.length === 0);
			}
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function startSignIn(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			pending = await api.credentials.signIn(provider);
			window.open(pending.url, '_blank', 'noopener');
			if (!pending.paste) poll();
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	// A device sign-in: ask the engine whether the person approved, at the
	// provider's own cadence, until it says yes (or the sign-in expires).
	function poll() {
		clearTimeout(timer);
		const p = pending;
		// A request can land after the panel closed: nothing would clear a new timer.
		if (!p || closed) return;
		timer = setTimeout(async () => {
			try {
				const c = await api.credentials.completeSignIn(p.id, undefined, name.trim() || undefined);
				if (c) {
					toast(t('providers.signedIn', { name: c.name }));
					pending = null;
					onadded([c], true);
					return;
				}
				poll();
			} catch (err) {
				error = errorMessage(err);
				pending = null;
			}
		}, p.intervalSeconds * 1000);
	}

	async function finishPaste(e: SubmitEvent) {
		e.preventDefault();
		if (!pending) return;
		busy = true;
		error = '';
		try {
			const c = await api.credentials.completeSignIn(
				pending.id,
				pasted.trim(),
				name.trim() || undefined
			);
			if (c) {
				toast(t('providers.signedIn', { name: c.name }));
				pending = null;
				onadded([c], true);
			}
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function copyCode() {
		if (!pending?.userCode) return;
		try {
			await navigator.clipboard.writeText(pending.userCode);
			toast(t('providers.copied'));
		} catch {
			/* the code is on screen to type */
		}
	}

	const uid = Math.random().toString(36).slice(2, 7);
</script>

{#if mode === 'key'}
	<form class="col" onsubmit={saveKey}>
		<div class="grid2">
			<div class="field">
				<label for="ac-{uid}-p">{t('providers.provider')}</label>
				<Select
					id="ac-{uid}-p"
					bind:value={provider}
					required
					options={keyable.map((p) => ({ value: p.id, label: p.name }))}
				/>
			</div>
			<div class="field">
				<label for="ac-{uid}-k">{t('providers.key')}</label>
				<input id="ac-{uid}-k" type="password" bind:value={key} required autocomplete="off" />
				<div class="help">{t('form.int.writeOnly')}</div>
			</div>
			<div class="field">
				<label for="ac-{uid}-n">{t('providers.name')}</label>
				<input id="ac-{uid}-n" bind:value={name} placeholder={t('providers.namePlaceholder')} />
			</div>
		</div>
		{#if error}<div class="error">{error}</div>{/if}
		<div class="form-actions">
			<button type="button" class="btn-ghost" onclick={oncancel}>{t('common.cancel')}</button>
			<button class="btn-primary" type="submit" disabled={busy || !provider} aria-busy={busy}
				>{busy ? t('providers.saving') : t('providers.save')}</button
			>
		</div>
	</form>
{:else if mode === 'signin'}
	{#if !pending}
		<form class="col" onsubmit={startSignIn}>
			<Callout>{t('providers.ownLogin')}</Callout>
			<div class="grid2">
				<div class="field">
					<label for="ac-{uid}-sp">{t('providers.provider')}</label>
					<Select
						id="ac-{uid}-sp"
						bind:value={provider}
						required
						options={signable.map((p) => ({ value: p.id, label: p.name }))}
					/>
				</div>
				<div class="field">
					<label for="ac-{uid}-sn">{t('providers.name')}</label>
					<input id="ac-{uid}-sn" bind:value={name} placeholder={t('providers.namePlaceholder')} />
				</div>
			</div>
			{#if error}<div class="error">{error}</div>{/if}
			<div class="form-actions">
				<button type="button" class="btn-ghost" onclick={oncancel}>{t('common.cancel')}</button>
				<button class="btn-primary" type="submit" disabled={busy || !provider} aria-busy={busy}
					>{busy ? t('providers.starting') : t('providers.start')}</button
				>
			</div>
		</form>
	{:else}
		<div class="col">
			<a
				class="btn btn-primary self-start"
				href={pending.url}
				target="_blank"
				rel="noreferrer noopener">{t('providers.open', { provider: nameOf(pending.provider) })}</a
			>
			{#if pending.userCode}
				<div class="field">
					<span class="caption">{t('providers.code')}</span>
					<div class="row">
						<code class="code-big mono">{pending.userCode}</code>
						<button type="button" class="btn-sm" onclick={copyCode}>{t('providers.copy')}</button>
					</div>
				</div>
				<div class="help">{t('providers.waiting')}</div>
			{/if}
			{#if pending.paste}
				<form class="col" onsubmit={finishPaste}>
					<div class="field">
						<label for="ac-{uid}-paste"
							>{t('providers.paste', { provider: nameOf(pending.provider) })}</label
						>
						<input
							id="ac-{uid}-paste"
							class="mono"
							bind:value={pasted}
							required
							autocomplete="off"
							placeholder={t('providers.pastePlaceholder')}
						/>
					</div>
					<div class="form-actions">
						<button type="button" class="btn-ghost" onclick={oncancel}>{t('common.cancel')}</button>
						<button class="btn-primary" type="submit" disabled={busy} aria-busy={busy}
							>{busy ? t('providers.finishing') : t('providers.finish')}</button
						>
					</div>
				</form>
			{:else}
				<div class="form-actions">
					<button type="button" class="btn-ghost" onclick={oncancel}>{t('common.cancel')}</button>
				</div>
			{/if}
			{#if error}<div class="error">{error}</div>{/if}
		</div>
	{/if}
{:else}
	<form class="col" onsubmit={importFile}>
		<Callout>{t('providers.ownLogin')}</Callout>
		<div class="field">
			<label for="ac-{uid}-file">{t('providers.importText')}</label>
			<textarea
				id="ac-{uid}-file"
				class="mono"
				rows="6"
				bind:value={text}
				required
				spellcheck="false"></textarea>
			<div class="help">{t('providers.importHelp')}</div>
		</div>
		{#if rejected.length}
			<div class="error">
				<strong>{t('providers.refused')}</strong>
				<ul>
					{#each rejected as r, i (i)}<li>{r}</li>{/each}
				</ul>
			</div>
		{/if}
		{#if error}<div class="error">{error}</div>{/if}
		<div class="form-actions">
			<button type="button" class="btn-ghost" onclick={oncancel}>{t('common.cancel')}</button>
			<button class="btn-primary" type="submit" disabled={busy} aria-busy={busy}
				>{t('providers.importAction')}</button
			>
		</div>
	</form>
{/if}

<style>
	.code-big {
		font-size: var(--fs-mark);
		letter-spacing: 0.12em;
		padding: 6px 12px;
		border-radius: var(--r-row);
		background: var(--fill-1);
		border: 1px solid var(--line-strong);
	}
	.self-start {
		align-self: flex-start;
	}
	ul {
		margin: 4px 0 0 18px;
	}
</style>
