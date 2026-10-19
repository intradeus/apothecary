<script lang="ts">
	// Members & roles (DESIGN 7.10). The engine has no user model yet: the one
	// local operator is the whole guild, and the table says so honestly.
	import { currentGuild, ME } from '$lib/guild';
	import { t } from '$lib/i18n/index.svelte';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import UserAvatar from '$lib/ui/UserAvatar.svelte';
	import { I } from '$lib/ui/icons';

	const guild = $derived(currentGuild());
	const ROLES = ['Owner', 'Maintainer', 'Engineer', 'Viewer'] as const;
</script>

<section class="c pane form-dense">
	<div class="pane-head">
		<h2>{t('page.system.members.title')}</h2>
		<span class="mut small">{t('page.members.one')}</span>
	</div>
	<div class="rows">
		<div class="r head mrow">
			<span></span><span>{t('page.members.name')}</span><span>{t('page.members.email')}</span><span
				>{t('page.members.role')}</span
			><span class="num">{t('page.members.lastActive')}</span>
		</div>
		<div class="r mrow">
			<UserAvatar name={ME.name} initials={ME.initials} presence />
			<span class="nm ell">{ME.name} <span class="you">{t('page.members.you')}</span></span>
			<span class="mut ell">{t('page.members.localOperator')}</span>
			<span>
				<select disabled aria-label={t('page.members.role')} value={guild.role}>
					{#each ROLES as r (r)}<option value={r}>{t(`page.members.role${r}`)}</option>{/each}
				</select>
			</span>
			<span class="num mut">{t('page.members.now')}</span>
		</div>
	</div>
</section>

<EmptyState title={t('page.members.soon')} text={t('page.members.soonText')}>
	<button class="btn-notyet" disabled>{I.add} {t('page.members.invite')}</button>
</EmptyState>

<style>
	.mrow {
		grid-template-columns: 30px minmax(0, 1.2fr) minmax(0, 1.4fr) 150px 90px;
	}
	.nm {
		font-weight: 600;
	}
	.you {
		font-weight: 500;
		font-size: var(--fs-meta);
		color: var(--mut);
		margin-left: 4px;
	}
	@media (max-width: 760px) {
		.mrow {
			grid-template-columns: 30px minmax(0, 1fr) 120px;
		}
		.mrow > :nth-child(3),
		.mrow > :nth-child(5) {
			display: none;
		}
	}
</style>
