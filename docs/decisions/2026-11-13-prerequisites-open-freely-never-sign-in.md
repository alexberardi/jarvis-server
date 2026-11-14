# A run opens apps and files to meet a prerequisite; it never signs in

- **Date:** 2026-11-13 (user; verified-replay Q1 of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

Before step 1 a run checks each prerequisite and tries to make an unmet one true:

- **App open:** the run launches it.
- **File open:** the run opens the file when exactly one file of that name is found (the places
  the recording used, then the usual document folders). With none or several it asks which.
  Asking before every file was the alternative; the user did not choose it ("A & B launch
  freely", then no objection to A when asked to pick).
- **Signed in:** the run checks. Signed out, it stops and asks the user to sign in, then
  continues. It never types a password.

**Signing in for the user is punted** (user: "that's gross. let's punt"). The user was not
comfortable with it and raised storing credentials on the node in a purpose-built table
encrypted behind K2. Notes for whoever picks it up:

- Storage on the node behind K2, in its own table (a site binding and its own access rule),
  not beside package secrets. The server never holds the secret.
- The hard part is use, not storage: the model never sees the password (it asks the node to
  fill the stored credential for a site); the node types only into a real password field on
  the exact stored site, since a page could otherwise talk a model-led run into leaking it;
  two-factor codes and CAPTCHAs still stop the run.
- A cheaper route: the browser's own password manager fills the fields and the run presses
  Sign in, so Jarvis holds nothing. Only for sites saved in the browser.

Strict or Creative (set per workflow) applies on top: Strict opens the file in its app as
recorded; Creative may meet the prerequisite another way.
