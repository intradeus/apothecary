<script lang="ts">
	// One's account (TODO 25): the profile, the password, personal API
	// tokens ($lib/system/TokenSection) and SSH keys (TODO 46). Everything
	// on this page is the signed-in user's own.
	import { errorMessage } from '$lib/core/errors';
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { session } from '$lib/app/session.svelte';
	import type { SshKey } from '$lib/domain';
	import { ago } from '$lib/core/format';
	import TokenSection from '$lib/system/TokenSection.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import { I } from '$lib/design/icons';

	// Profile.
	let handle = $state(session.user?.handle ?? '');
	let email = $state(session.user?.email ?? '');
	let name = $state(session.user?.name ?? '');
	let profileBusy = $state(false);
	let profileError = $state('');

	async function saveProfile(e: SubmitEvent) {
		e.preventDefault();
		profileBusy = true;
		profileError = '';
		try {
			await api.auth.updateMe({ handle: handle.trim(), email: email.trim(), name: name.trim() });
			await session.refresh();
			toast(t('page.account.saved'));
		} catch (err) {
			profileError = errorMessage(err);
		} finally {
			profileBusy = false;
		}
	}

	// Password.
	let current = $state('');
	let next = $state('');
	let again = $state('');
	let pwBusy = $state(false);
	let pwError = $state('');

	async function changePassword(e: SubmitEvent) {
		e.preventDefault();
		if (next !== again) {
			pwError = t('auth.setup.mismatch');
			return;
		}
		pwBusy = true;
		pwError = '';
		try {
			await api.auth.changePassword(current, next);
			current = next = again = '';
			toast(t('page.account.changed'));
		} catch (err) {
			pwError = errorMessage(err);
		} finally {
			pwBusy = false;
		}
	}

	// SSH keys (TODO 46): what the engine's SSH door lets one into a run's
	// workspace with. A key is added from its .pub line and known by its
	// fingerprint afterwards.
	let keys = $state<SshKey[]>([]);
	let keysLoaded = $state(false);
	let keysError = $state('');
	let keyLine = $state('');
	let keyName = $state('');
	let adding = $state(false);
	let removing = $state<string | null>(null);

	const loadKeys = async () => {
		try {
			keys = await api.auth.sshKeys();
			keysLoaded = true;
			keysError = '';
		} catch (err) {
			keysError = errorMessage(err);
		}
	};
	onMount(loadKeys);

	async function addKey(e: SubmitEvent) {
		e.preventDefault();
		adding = true;
		keysError = '';
		try {
			await api.auth.addSshKey(keyLine.trim(), keyName.trim());
			keyLine = keyName = '';
			toast(t('page.account.sshAdded'));
			await loadKeys();
		} catch (err) {
			keysError = errorMessage(err);
		} finally {
			adding = false;
		}
	}

	async function removeKey(k: SshKey) {
		const ok = await confirmDialog({
			title: t('page.account.sshRemove') + ` · ${k.name}`,
			body: k.fingerprint,
			action: t('page.account.sshRemove')
		});
		if (!ok) return;
		removing = k.id;
		try {
			await api.auth.removeSshKey(k.id);
			toast(t('page.account.sshRemoved'));
			await loadKeys();
		} catch (err) {
			keysError = errorMessage(err);
		} finally {
			removing = null;
		}
	}
</script>

<div class="sections">
	<section class="glass pane form-dense">
		<div class="pane-head">
			<h2>{t('page.account.profile')}</h2>
			<span class="muted small">{t('page.account.profileSub')}</span>
		</div>
		<form class="pane-body col" onsubmit={saveProfile}>
			<div class="grid2">
				<div class="field">
					<label for="ac-handle">{t('auth.handle')}</label>
					<input id="ac-handle" class="mono" bind:value={handle} required autocomplete="username" />
					<div class="help">{t('auth.handleHelp')}</div>
				</div>
				<div class="field">
					<label for="ac-name">{t('auth.name')}</label>
					<input id="ac-name" bind:value={name} autocomplete="name" />
				</div>
			</div>
			<div class="field">
				<label for="ac-email">{t('auth.email')}</label>
				<input id="ac-email" type="email" bind:value={email} required autocomplete="email" />
			</div>
			<div class="help">
				{t('page.account.methods', { methods: session.me?.methods.join(', ') ?? '' })}
			</div>
			{#if profileError}<div class="error">{profileError}</div>{/if}
			<div class="form-actions">
				<button class="btn-primary" type="submit" disabled={profileBusy} aria-busy={profileBusy}
					>{t('page.account.save')}</button
				>
			</div>
		</form>
	</section>

	<section class="glass pane form-dense">
		<div class="pane-head">
			<h2>{t('page.account.password')}</h2>
			<span class="muted small">{t('page.account.passwordSub')}</span>
		</div>
		<form class="pane-body col" onsubmit={changePassword}>
			<div class="field">
				<label for="ac-cur">{t('page.account.current')}</label>
				<input
					id="ac-cur"
					type="password"
					bind:value={current}
					required
					autocomplete="current-password"
				/>
			</div>
			<div class="grid2">
				<div class="field">
					<label for="ac-new">{t('page.account.new')}</label>
					<input
						id="ac-new"
						type="password"
						bind:value={next}
						required
						minlength="8"
						autocomplete="new-password"
					/>
					<div class="help">{t('auth.passwordHelp')}</div>
				</div>
				<div class="field">
					<label for="ac-again">{t('auth.passwordAgain')}</label>
					<input
						id="ac-again"
						type="password"
						bind:value={again}
						required
						autocomplete="new-password"
					/>
				</div>
			</div>
			{#if pwError}<div class="error">{pwError}</div>{/if}
			<div class="form-actions">
				<button class="btn-primary" type="submit" disabled={pwBusy} aria-busy={pwBusy}
					>{t('page.account.change')}</button
				>
			</div>
		</form>
	</section>

	<TokenSection />

	<section class="glass pane form-dense">
		<div class="pane-head">
			<h2>{t('page.account.ssh')}</h2>
			<span class="muted small">{t('page.account.sshSub')}</span>
		</div>
		<div class="pane-body col">
			<form class="col" onsubmit={addKey}>
				<div class="field">
					<label for="ac-sshkey">{t('page.account.sshKey')}</label>
					<textarea
						id="ac-sshkey"
						class="mono pubkey"
						rows={2}
						bind:value={keyLine}
						required
						spellcheck="false"
						placeholder={t('page.account.sshKeyPlaceholder')}></textarea>
					<div class="help">{t('page.account.sshKeyHelp')}</div>
				</div>
				<div class="row nowrap mint">
					<div class="field grow">
						<label for="ac-sshname">{t('page.account.sshName')}</label>
						<input
							id="ac-sshname"
							bind:value={keyName}
							placeholder={t('page.account.sshNamePlaceholder')}
						/>
					</div>
					<button class="btn-primary" type="submit" disabled={adding} aria-busy={adding}
						>{I.add} {t('page.account.sshAdd')}</button
					>
				</div>
			</form>
			{#if keysError}<ErrorCard cause={t('page.account.failed')} detail={keysError} />{/if}
			{#if !keysLoaded}
				<Skeleton lines={2} />
			{:else if keys.length}
				<div class="rows">
					{#each keys as k (k.id)}
						<div class="r krow">
							<span class="nm truncate" title={k.publicKey}>{k.name}</span>
							<span class="mono muted small truncate" title={k.fingerprint}
								>{k.type} · {k.fingerprint}</span
							>
							<span class="muted small truncate">
								{t('page.account.created', { ago: ago(k.createdAt) })}
								· {k.lastUsedAt
									? t('page.account.lastUsed', { ago: ago(k.lastUsedAt) })
									: t('page.account.neverUsed')}
							</span>
							<button
								class="btn-danger btn-sm"
								aria-busy={removing === k.id}
								onclick={() => removeKey(k)}>{t('page.account.sshRemove')}</button
							>
						</div>
					{/each}
				</div>
			{:else}
				<EmptyState
					compact
					title={t('page.account.noSshKeys')}
					text={t('page.account.noSshKeysText')}
				/>
			{/if}
		</div>
	</section>
</div>

<style>
	.mint {
		align-items: flex-end;
		gap: 10px;
	}
	.mint .field {
		margin: 0;
	}
	.krow {
		grid-template-columns: minmax(0, 1fr) minmax(0, 1.8fr) minmax(0, 1.2fr) auto;
		align-items: center;
	}
	.nm {
		font-weight: 600;
	}
	/* A .pub line is one long word: wrap it anywhere rather than scroll. */
	.pubkey {
		resize: vertical;
		min-height: 54px;
		font-size: var(--fs-label);
		overflow-wrap: anywhere;
	}
	@media (max-width: 760px) {
		.krow {
			grid-template-columns: minmax(0, 1fr) auto;
		}
		.krow > :nth-child(2),
		.krow > :nth-child(3) {
			display: none;
		}
	}
</style>
