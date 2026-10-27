<script lang="ts">
	// Users (TODO 25), for the instance admin: every account, with what an
	// admin may do to one — create it, make or unmake an admin, disable it,
	// set its password. Nothing here looks into anyone's work.
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { session } from '$lib/session.svelte';
	import type { User } from '$lib/domain';
	import { ago } from '$lib/format';
	import { initialsOf } from '$lib/urls';
	import { t } from '$lib/i18n/index.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import AddCard from '$lib/system/AddCard.svelte';
	import Checkbox from '$lib/ui/Checkbox.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import SectionHeader from '$lib/ui/SectionHeader.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import UserAvatar from '$lib/ui/UserAvatar.svelte';
	import { I } from '$lib/ui/icons';

	let users = $state<User[]>([]);
	let loaded = $state(false);
	let loadError = $state('');
	let error = $state('');
	let adding = $state(false);
	let busy = $state<string | null>(null);

	// The new-account form.
	let handle = $state('');
	let email = $state('');
	let name = $state('');
	let password = $state('');
	let admin = $state(false);

	// The password reset, inline under one row.
	let resetting = $state<string | null>(null);
	let newPassword = $state('');

	const refresh = async () => {
		try {
			users = await api.users.list();
			loaded = true;
			loadError = '';
		} catch (err) {
			loadError = err instanceof Error ? err.message : String(err);
		}
	};
	onMount(refresh);

	async function create(e: SubmitEvent) {
		e.preventDefault();
		busy = 'new';
		error = '';
		try {
			const u = await api.users.create({
				handle: handle.trim(),
				email: email.trim(),
				name: name.trim(),
				password,
				admin
			});
			handle = email = name = password = '';
			admin = false;
			adding = false;
			toast(t('page.users.created', { handle: u.handle }));
			await refresh();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = null;
		}
	}

	async function patch(u: User, d: { admin?: boolean; disabled?: boolean }) {
		busy = u.id;
		error = '';
		try {
			await api.users.update(u.id, d);
			toast(t('page.users.updated', { handle: u.handle }));
			await refresh();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = null;
		}
	}

	async function disable(u: User) {
		const ok = await confirmDialog({
			title: t('page.users.disableTitle', { handle: u.handle }),
			body: t('page.users.disableBody'),
			action: t('page.users.disableAction')
		});
		if (ok) await patch(u, { disabled: true });
	}

	async function reset(e: SubmitEvent, u: User) {
		e.preventDefault();
		busy = u.id;
		error = '';
		try {
			await api.users.resetPassword(u.id, newPassword);
			newPassword = '';
			resetting = null;
			toast(t('page.users.passwordSet', { handle: u.handle }));
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = null;
		}
	}

	const me = $derived(session.user?.id);
</script>

{#if loadError && !loaded}
	<ErrorCard cause={t('page.users.loadError')} detail={loadError} onretry={refresh} />
{:else if !loaded}
	<Skeleton pane lines={4} />
{:else}
	<SectionHeader title={t('page.system.users.title')} count={users.length}>
		{#snippet actions()}
			<button
				class="btn-sm"
				class:btn-sel={adding}
				aria-pressed={adding}
				onclick={() => (adding = !adding)}
				>{adding ? t('page.users.close') : `${I.add} ${t('page.users.add')}`}</button
			>
		{/snippet}
	</SectionHeader>

	{#if error}<ErrorCard cause={t('page.users.failed')} detail={error} ondismiss={() => (error = '')} />{/if}

	{#if adding}
		<AddCard title={t('page.users.new')}>
			<form class="col form-dense" onsubmit={create}>
				<div class="grid2">
					<div class="field">
						<label for="nu-handle">{t('auth.handle')}</label>
						<input id="nu-handle" class="mono" bind:value={handle} required autocomplete="off" />
						<div class="help">{t('auth.handleHelp')}</div>
					</div>
					<div class="field">
						<label for="nu-name">{t('auth.name')}</label>
						<input id="nu-name" bind:value={name} autocomplete="off" />
					</div>
					<div class="field">
						<label for="nu-email">{t('auth.email')}</label>
						<input id="nu-email" type="email" bind:value={email} required autocomplete="off" />
					</div>
					<div class="field">
						<label for="nu-pw">{t('page.users.initialPassword')}</label>
						<input
							id="nu-pw"
							type="password"
							bind:value={password}
							required
							minlength="8"
							autocomplete="new-password"
						/>
						<div class="help">{t('page.users.initialPasswordHelp')}</div>
					</div>
				</div>
				<Checkbox bind:checked={admin} label={t('page.users.admin')} />
				<div class="help">{t('page.users.adminHelp')}</div>
				<div class="form-actions">
					<button type="button" class="btn-ghost" onclick={() => (adding = false)}
						>{t('common.cancel')}</button
					>
					<button class="btn-primary" type="submit" disabled={busy === 'new'} aria-busy={busy === 'new'}
						>{busy === 'new' ? t('page.users.creating') : t('page.users.create')}</button
					>
				</div>
			</form>
		</AddCard>
	{/if}

	{#if users.length}
		<section class="c pane">
			<div class="rows">
				<div class="r head urow">
					<span></span><span>{t('page.users.name')}</span><span>{t('page.users.email')}</span><span
						>{t('page.users.role')}</span
					><span class="num">{t('page.users.lastSeen')}</span><span></span>
				</div>
				{#each users as u (u.id)}
					<div class="r urow" class:off={u.disabled}>
						<UserAvatar name={u.name} initials={initialsOf(u.name)} presence={u.id === me} />
						<span class="col nm">
							<span class="ell"
								>{u.name}
								{#if u.id === me}<span class="you">{t('page.users.you')}</span>{/if}</span
							>
							<span class="mono mut small">@{u.handle}</span>
						</span>
						<span class="mut ell">{u.email}</span>
						<span class="chips">
							{#if u.admin}<span class="pill sm t-parch">{t('page.users.admin')}</span
								>{:else}<span class="pill sm flat">{t('page.users.member')}</span>{/if}
							{#if u.disabled}<span class="pill sm t-rose">{t('page.users.disabled')}</span>{/if}
						</span>
						<span class="num mut small">{u.lastSeenAt ? ago(u.lastSeenAt) : t('page.users.never')}</span>
						<span class="row nowrap acts">
							{#if u.id !== me}
								<button
									class="btn-sm"
									aria-busy={busy === u.id}
									onclick={() => patch(u, { admin: !u.admin })}
									>{u.admin ? t('page.users.dropAdmin') : t('page.users.makeAdmin')}</button
								>
								{#if u.disabled}
									<button class="btn-sm" aria-busy={busy === u.id} onclick={() => patch(u, { disabled: false })}
										>{t('page.users.enable')}</button
									>
								{:else}
									<button class="btn-danger btn-sm" aria-busy={busy === u.id} onclick={() => disable(u)}
										>{t('page.users.disable')}</button
									>
								{/if}
							{/if}
							<button
								class="btn-sm"
								class:btn-sel={resetting === u.id}
								onclick={() => (resetting = resetting === u.id ? null : u.id)}
								>{t('page.users.resetPassword')}</button
							>
						</span>
					</div>
					{#if resetting === u.id}
						<form class="r reset form-dense" onsubmit={(e) => reset(e, u)}>
							<div class="field grow">
								<label for="rp-{u.id}">{t('page.users.resetTitle', { handle: u.handle })}</label>
								<input
									id="rp-{u.id}"
									type="password"
									bind:value={newPassword}
									required
									minlength="8"
									autocomplete="new-password"
								/>
								<div class="help">{t('page.users.resetHelp')}</div>
							</div>
							<button class="btn-primary btn-sm" type="submit" disabled={busy === u.id} aria-busy={busy === u.id}
								>{t('page.users.resetAction')}</button
							>
						</form>
					{/if}
				{/each}
			</div>
		</section>
	{:else}
		<EmptyState title={t('page.users.count', { count: 0 })} />
	{/if}
{/if}

<style>
	.urow {
		grid-template-columns: 30px minmax(0, 1.1fr) minmax(0, 1.3fr) auto 90px auto;
		align-items: center;
	}
	.urow.off {
		opacity: 0.6;
	}
	.nm {
		gap: 0;
		line-height: 1.2;
	}
	.you {
		font-weight: 500;
		font-size: var(--fs-meta);
		color: var(--mut);
		margin-left: 4px;
	}
	.acts {
		gap: 6px;
		justify-content: flex-end;
	}
	.reset {
		display: flex;
		align-items: flex-end;
		gap: 10px;
		padding: 8px 16px 14px;
	}
	.reset .field {
		margin: 0;
	}
	@media (max-width: 760px) {
		.urow {
			grid-template-columns: 30px minmax(0, 1fr) auto;
		}
		.urow > :nth-child(3),
		.urow > :nth-child(4),
		.urow > :nth-child(5) {
			display: none;
		}
		.acts {
			flex-wrap: wrap;
		}
	}
</style>
