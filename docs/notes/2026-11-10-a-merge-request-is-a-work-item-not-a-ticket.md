# A merge request is a work item, not a ticket (2026-11-10)

- A run used to need a ticket, so starting one on a merge request found or
  made a ticket keyed by the merge request's URL. That covered the
  workshop's Start button and the merge_request automation. The engine
  never needed the ticket: a run without one was already supported (tracker
  stages skip it, visibly). The ticket only grouped runs, deduplicated the
  automation and enforced one active run.
- A run on a merge request now has no ticket. `tickets.MRWork` turns the
  merge request into the run's work: title "<title> (!iid by author)", and
  the description plus a line naming the merge request as the goal.
  `ForMR`/`CreateForMR` are gone. Merge request tickets made before this
  stay as plain tickets.
- A merge request's runs are the runs that have it in their data: the one
  they were started on (`View.Adopted`) or the one they opened
  (`View.MRs`, from `mr_created`). `View.MRIIDs`/`HasMR` and
  `Engine.RunsOnMR(s)` find them. The UI does the same over the fleet poll
  (`$lib/mrs/runsOnMR`).
- One active run per merge request (`ErrMRBusy`, under an `mrLock` like the
  ticket lock) applies to every run started on one, a ticket's run
  included: two runs pushing to one branch undo each other. The HTTP layer
  answers both refusals the same way: a 409 naming the run in the way
  (`runBusy`).
- The automation skips a merge request any run has been through, whoever
  started it. Before, it skipped one with a ticket.
- "Opened by the factory" compared the merge request's author with the
  repository integration's account. With a personal token that marked a
  person's own merge requests, and it said nothing about merge requests the
  factory had only reviewed. It is replaced by "processed by apothecary
  N times", the count of runs that have the merge request in their data.
  `MRRow.Mine`/`TicketID` are gone. `RepoHostUser` still keeps the
  automation off the integration's own merge requests.
- The UI treats merge requests as the second work item:
  - **Sidebar:** a "Merge requests" entry beside Tickets (`G M`), opening
    `/merge-requests`, every workshop's open ones.
  - **Merge request page:** `/merge-requests/[project]/[iid]` (`?repo=`) is
    the ticket page's twin. It shows the description and starts a run with
    only the flows that begin on a merge request (`GET
    /api/projects/{id}/mrs/{iid}`).
  - **New run:** Ticket and Merge request tabs.
  - **Run header:** a ticketless merge request run links its merge request.
