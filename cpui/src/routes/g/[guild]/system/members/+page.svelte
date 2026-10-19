<script lang="ts">
	// Members & roles (DESIGN 7.10). The engine has no user model yet: the one
	// local operator is the whole guild, and the table says so honestly.
	import { currentGuild, ME } from '$lib/guild';
	import EmptyState from '$lib/ui/EmptyState.svelte';
	import UserAvatar from '$lib/ui/UserAvatar.svelte';
	import { I } from '$lib/ui/icons';

	const guild = $derived(currentGuild());
	const ROLES = ['Owner', 'Maintainer', 'Engineer', 'Viewer'] as const;
</script>

<section class="c pane form-dense">
	<div class="pane-head">
		<h2>Members</h2>
		<span class="mut small">1 member</span>
	</div>
	<div class="rows">
		<div class="r head mrow">
			<span></span><span>Name</span><span>Email</span><span>Role</span><span class="num"
				>Last active</span
			>
		</div>
		<div class="r mrow">
			<UserAvatar name={ME.name} initials={ME.initials} presence />
			<span class="nm ell">{ME.name} <span class="you">you</span></span>
			<span class="mut ell">local operator · no sign-in</span>
			<span>
				<select disabled aria-label="Role" value={guild.role}>
					{#each ROLES as r (r)}<option value={r}>{r}</option>{/each}
				</select>
			</span>
			<span class="num mut">now</span>
		</div>
	</div>
</section>

<EmptyState
	title="Members arrive with multi-user support"
	text="This engine runs for one operator. Invites, roles and per-guild access come with accounts."
>
	<button class="btn-notyet" disabled>{I.add} Invite</button>
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
