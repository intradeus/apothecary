- 23) new run on observability + analytics dashboards per run/flow/skills : fastest, cheapest. Add a scoring system for the results maybe ?
- 36) Resquash migrations
- 43) SECURITY
- 49) Ticket support for status, labels, comments etc. like source tracker. Syncing source ticket data to internal ticket, if there is a source. If not, move the ticket itself to done / add label / comments.
- 50) Support for advisor / secondary models (omp support only for example) in the flow. This will bring a new feature : harness-specific parameters for agentic stages. This might mean that we have to move away from $parameters for harnesses.

------ 

After 1.0.0 : 
- 39) RAW, durable workspaces. For fully hands-on work. Connectable via SSH using remote VScode. Goal : no more local compute. Apotehcary provisions the workspace as usual, but no flow is ever started. User gets either a 'connect to code-server' or a URL to ssh into it with desktop vscode. Extensions and harnesses are already installed, so user can still use AI, but just more manually, with remote compute AND everything should still work in the background after closing desktop vscode. Either this is a new non-flow feature OR we add a flow type where main only takes : manual and that's it. There's no end to it. But in a more complex flow, from the task's source and the branch created (as always), apothecary can still poll and get any MR related and close the workspace once the MR is merged, and move the tickets etc. BASICALLY just work on a "manual" (or "semi-automated") flow or stage only maybe, with 0 agent stages, only human coding.
- 48) "Queues" : queue tasks, for automatic resolution