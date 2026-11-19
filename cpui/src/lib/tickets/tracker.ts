// A workshop's ticket source as the pages need it: whether tickets are
// written here (the internal tracker). Where a remote tracker lives comes
// with the workshop (Project.ticketSource.home).

import { INTERNAL_TRACKER, type TicketSource } from '$lib/domain';

/** Tickets are written here only on the internal tracker; a remote tracker
 *  takes new issues itself. */
export const isInternal = (source: Pick<TicketSource, 'kind'> | undefined): boolean =>
	source?.kind === INTERNAL_TRACKER;
