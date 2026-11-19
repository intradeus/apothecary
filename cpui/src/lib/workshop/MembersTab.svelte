<script lang="ts" module>
	import type { ProjectRole } from '$lib/domain';
	import { t } from '$lib/i18n/index.svelte';

	/** The label of a workshop role: Owner, Maintainer, Operator, Viewer. */
	export const roleLabel = (role: ProjectRole): string =>
		role === 'owner'
			? t('workshop.members.owner')
			: role === 'maintainer'
				? t('workshop.members.roleMaintainer')
				: role === 'operator'
					? t('workshop.members.roleOperator')
					: t('workshop.members.roleViewer');
</script>

<script lang="ts">
	// Members tab: who else works in this workshop. The owner heads the list;
	// a caller holding project:members adds, re-roles and removes members
	// (maintainers only by the owner); a member may leave (project:leave).
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import {
		MEMBER_ROLES,
		projectCan,
		type Member,
		type MemberRole,
		type Project
	} from '$lib/domain';
	import { session } from '$lib/app/session.svelte';
	import { fleet } from '$lib/app/fleet.svelte';
	import { errorMessage } from '$lib/core/errors';
	import { to } from '$lib/core/urls';
	import { confirmDialog } from '$lib/ui/confirm.svelte';
	import { toast } from '$lib/ui/toast.svelte';
	import ErrorCard from '$lib/ui/ErrorCard.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import Skeleton from '$lib/ui/Skeleton.svelte';
	import Select from '$lib/ui/Select.svelte';
	import { I } from '$lib/design/icons';

	let { project }: { project: Project } = $props();

	let members = $state<Member[] | null>(null);
	let error = $state('');
	let login = $state('');
	let role = $state<MemberRole>('viewer');
	let addError = $state('');
	let busy = $state(false);
	/** Bumped to remount the role Selects when a change is cancelled or refused. */
	let rev = $state(0);

	const owner = $derived(project.access.role === 'owner');
	const manage = $derived(projectCan(project, 'project:members'));
	const mayLeave = $derived(projectCan(project, 'project:leave'));
	const ownerHandle = $derived(project.ownerHandle ?? session.user?.handle ?? '');
	const ownerName = $derived(project.ownerHandle ? '' : (session.user?.name ?? ''));
	const roleOptions = $derived(
		MEMBER_ROLES.filter((r) => owner || r !== 'maintainer').map((r) => ({
			value: r,
			label: roleLabel(r)
		}))
	);
	/** A non-owner may not touch a maintainer. */
	const editable = (m: Member) => manage && (owner || m.role !== 'maintainer');

	async function load() {
		try {
			members = await api.projects.members(project.id);
			error = '';
		} catch (e) {
			error = errorMessage(e);
		}
	}
	onMount(() => void load());

	async function add(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		try {
			const m = await api.projects.addMember(project.id, login.trim(), role);
			members = [...(members ?? []), m];
			toast(t('workshop.members.added', { handle: m.handle }));
			login = '';
			addError = '';
		} catch (err) {
			addError = errorMessage(err);
		} finally {
			busy = false;
		}
	}

	async function change(m: Member, next: MemberRole) {
		const ok = await confirmDialog({
			title: t('workshop.members.changeTitle', { handle: m.handle, role: roleLabel(next) }),
			body: t('workshop.members.changeBody'),
			action: t('workshop.members.changeAction')
		});
		if (!ok) {
			rev++;
			return;
		}
		try {
			const u = await api.projects.updateMember(project.id, m.userId, next);
			members = (members ?? []).map((x) => (x.userId === u.userId ? u : x));
			toast(t('workshop.members.changed', { handle: u.handle, role: roleLabel(u.role) }));
		} catch (err) {
			rev++;
			toast(errorMessage(err), { tone: 'bad' });
		}
	}

	async function remove(m: Member) {
		const ok = await confirmDialog({
			title: t('workshop.members.removeTitle', { handle: m.handle }),
			body: t('workshop.members.removeBody'),
			action: t('workshop.members.remove')
		});
		if (!ok) return;
		try {
			await api.projects.removeMember(project.id, m.userId);
			members = (members ?? []).filter((x) => x.userId !== m.userId);
			toast(t('workshop.members.removed', { handle: m.handle }));
		} catch (err) {
			toast(errorMessage(err), { tone: 'bad' });
		}
	}

	async function leave() {
		const ok = await confirmDialog({
			title: t('workshop.members.leaveTitle', { name: project.name }),
			body: t('workshop.members.leaveBody'),
			action: t('workshop.members.leave')
		});
		if (!ok) return;
		try {
			await api.projects.leave(project.id);
			toast(t('workshop.members.left', { name: project.name }));
			await fleet.refresh();
			await goto(to.workshops());
		} catch (err) {
			toast(errorMessage(err), { tone: 'bad' });
		}
	}
</script>

<p class="lede">{t('workshop.members.lede')}</p>
{#if mayLeave}
	<div class="leave">
		<button class="btn-sm btn-danger" onclick={leave}>{t('workshop.members.leave')}</button>
	</div>
{/if}
{#if manage}
	<form class="add pane" onsubmit={add}>
		<div class="field grow">
			<label for="mb-login">{t('workshop.members.login')}</label>
			<input
				id="mb-login"
				bind:value={login}
				placeholder={t('workshop.members.loginPlaceholder')}
				autocomplete="off"
				required
			/>
		</div>
		<div class="field">
			<label for="mb-role">{t('workshop.members.role')}</label>
			<Select id="mb-role" bind:value={role} options={roleOptions} />
		</div>
		<button class="btn-primary" type="submit" disabled={busy || !login.trim()}
			>{I.add} {t('workshop.members.add')}</button
		>
		{#if addError}<p class="error" role="alert">{addError}</p>{/if}
	</form>
{/if}
{#if error && !members}
	<ErrorCard cause={t('workshop.members.loadError')} detail={error} onretry={load} />
{:else if !members}
	<Skeleton pane lines={3} />
{:else}
	<section class="pane">
		{#key rev}
			<ul class="rows" aria-label={t('workshop.members.title')}>
				<li class="r">
					<span class="mono">@{ownerHandle}</span>
					<span class="nm"
						>{ownerName}{#if !project.ownerHandle}
							<span class="faint">({t('workshop.members.you')})</span>{/if}</span
					>
					<span class="role">{roleLabel('owner')}</span>
					<span></span>
				</li>
				{#each members as m (m.userId)}
					<li class="r">
						<span class="mono">@{m.handle}</span>
						<span class="nm">{m.name}</span>
						{#if editable(m)}
							<span class="role">
								<Select
									size="sm"
									inline
									label={t('workshop.members.roleOf', { handle: m.handle })}
									value={m.role}
									options={roleOptions}
									onchange={(v) => change(m, v)}
								/>
							</span>
							<span class="acts">
								<button
									class="btn-sm"
									aria-label={t('workshop.members.removeLabel', { handle: m.handle })}
									onclick={() => remove(m)}>{t('workshop.members.remove')}</button
								>
							</span>
						{:else}
							<span class="role">{roleLabel(m.role)}</span>
							<span></span>
						{/if}
					</li>
				{/each}
			</ul>
		{/key}
		{#if !members.length}
			<EmptyState
				compact
				title={t('workshop.members.empty')}
				text={t('workshop.members.emptyText')}
			/>
		{/if}
	</section>
{/if}

<style>
	.leave {
		display: flex;
		justify-content: flex-end;
		margin-bottom: 12px;
	}
	.add {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: 10px;
		margin-bottom: 16px;
	}
	.add .grow {
		flex: 1 1 220px;
	}
	.add .error {
		flex-basis: 100%;
		margin: 0;
	}
	.rows {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.r {
		display: grid;
		grid-template-columns: minmax(120px, 1fr) minmax(120px, 1.5fr) 160px auto;
		align-items: center;
		gap: 12px;
		padding: 8px 4px;
		border-bottom: 1px solid var(--line);
	}
	.acts {
		justify-self: end;
	}
</style>
