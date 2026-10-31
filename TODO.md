- 37) frontend refactor + new design. Full test suite.
- 23) new run on observability (full flow data + resource stats)
- 36) Resquash migrations
- 43) SECURITY

------ 

After 1.0.0 : 
- 39) RAW, durable workspaces. For fully hands-on work. Connectable via SSH using remote VScode. Goal : no more local compute. Apotehcary provisions the workspace as usual, but no flow is ever started. User gets either a 'connect to code-server' or a URL to ssh into it with desktop vscode. Extensions and harnesses are already installed, so user can still use AI, but just more manually, with remote compute AND everything should still work in the background after closing desktop vscode. Either this is a new non-flow feature OR we add a flow type where main only takes : manual and that's it. There's no end to it. But in a more complex flow, from the task's source and the branch created (as always), apothecary can still poll and get any MR related and close the workspace once the MR is merged, and move the tickets etc. BASICALLY just work on a "manual" (or "semi-automated") flow or stage only maybe, with 0 agent stages, only human coding.