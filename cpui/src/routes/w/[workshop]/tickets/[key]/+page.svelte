<!-- A workshop's ticket, by its key on the workshop's source. -->
<script lang="ts">
	import { page } from '$app/state';
	import TicketPage from '$lib/workshop/TicketPage.svelte';

	// The layout keys the page on both params: they never change under it.
	const projectId = page.params.workshop ?? '';
	const ticketKey = page.params.key ?? '';
</script>

<TicketPage {projectId} {ticketKey} />
